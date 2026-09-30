package manifest

import (
	"errors"
	"path"
	"strings"
	"testing"
)

var digest = make([]byte, 32)

func file(p string, size int64) Item {
	return Item{Path: p, Kind: KindFile, Size: size, SHA256: digest}
}
func dir(p string) Item { return Item{Path: p, Kind: KindDir} }

func TestValidateAccepts(t *testing.T) {
	cases := map[string]Manifest{
		"single file":  {Items: []Item{file("photo.png", 10)}},
		"empty file":   {Items: []Item{file("empty", 0)}},
		"folder":       {Items: []Item{dir("album"), file("album/a.jpg", 1), dir("album/raw"), file("album/raw/a.cr3", 2)}},
		"implicit dir": {Items: []Item{file("a/b/c.txt", 1)}},
		"unicode":      {Items: []Item{file("café/naïve.txt", 1)}},
		"dots in name": {Items: []Item{file("..hidden", 1), file("a..b", 1)}},
		"clipboard":    {Items: []Item{{Path: "clipboard.txt", Kind: KindText, Size: 5, SHA256: digest, Mime: "text/plain"}}},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if err := m.Validate(Default); err != nil {
				t.Fatalf("rejected: %v", err)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]Manifest{
		"no items":            {},
		"parent escape":       {Items: []Item{file("../etc/passwd", 1)}},
		"inner escape":        {Items: []Item{file("a/../../b", 1)}},
		"absolute":            {Items: []Item{file("/etc/passwd", 1)}},
		"backslash":           {Items: []Item{file(`..\..\win.ini`, 1)}},
		"nul":                 {Items: []Item{file("a\x00b", 1)}},
		"control char":        {Items: []Item{file("a\nb", 1)}},
		"dot component":       {Items: []Item{file("./a", 1)}},
		"trailing slash":      {Items: []Item{file("a/", 1)}},
		"double slash":        {Items: []Item{file("a//b", 1)}},
		"empty path":          {Items: []Item{file("", 1)}},
		"bad utf8":            {Items: []Item{file("\xff", 1)}},
		"duplicate":           {Items: []Item{file("a", 1), file("a", 1)}},
		"case duplicate":      {Items: []Item{file("Photo.PNG", 1), file("photo.png", 1)}},
		"unicode duplicate":   {Items: []Item{file("café", 1), file("café", 1)}},
		"file used as dir":    {Items: []Item{file("a", 1), file("a/b", 1)}},
		"file as deep parent": {Items: []Item{file("a/b", 1), file("a/b/c/d", 1)}},
		"negative size":       {Items: []Item{file("a", -1)}},
		"short digest":        {Items: []Item{{Path: "a", Kind: KindFile, Size: 1, SHA256: []byte{1}}}},
		"dir with size":       {Items: []Item{{Path: "a", Kind: KindDir, Size: 3}}},
		"unknown kind":        {Items: []Item{{Path: "a", Kind: 9, SHA256: digest}}},
		"symlink-ish kind":    {Items: []Item{{Path: "a", Kind: 0, SHA256: digest}}},
		"too deep":            {Items: []Item{file(strings.Repeat("d/", 70)+"f", 1)}},
		"path too long":       {Items: []Item{file(strings.Repeat("x", 2000), 1)}},
		"total overflow":      {Items: []Item{file("a", 1<<62), file("b", 1<<62), file("c", 1<<62)}},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			err := m.Validate(Default)
			if err == nil {
				t.Fatal("accepted")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v does not wrap ErrInvalid", err)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	l := Default
	l.MaxItems, l.MaxBytes = 2, 100
	if err := (Manifest{Items: []Item{file("a", 1), file("b", 1), file("c", 1)}}).Validate(l); err == nil {
		t.Fatal("item limit not enforced")
	}
	if err := (Manifest{Items: []Item{file("a", 60), file("b", 60)}}).Validate(l); err == nil {
		t.Fatal("byte limit not enforced")
	}
}

func TestRoundTrip(t *testing.T) {
	m := Manifest{Items: []Item{dir("d"), file("d/x", 3)}}
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, Default)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Items[1].Path != "d/x" || got.Total() != 3 {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

// Anything Decode accepts must be safe to join under a destination: every
// path stays below it, and no two items fold to the same name.
func FuzzDecode(f *testing.F) {
	for _, m := range []Manifest{
		{Items: []Item{file("a", 1)}},
		{Items: []Item{dir("d"), file("d/e", 2)}},
		{Items: []Item{file("../x", 1)}},
	} {
		b, _ := m.Encode()
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := Decode(b, Default)
		if err != nil {
			return
		}
		checkSafe(t, m)
	})
}

// FuzzPath drives the path rules directly, which reaches far more of them
// than mutating CBOR bytes does.
func FuzzPath(f *testing.F) {
	for _, s := range []string{"a", "a/b", "../a", "a/../b", "/a", `a\b`, "a//b", "CON", "a/./b", "é"} {
		f.Add(s, "b")
	}
	f.Fuzz(func(t *testing.T, p1, p2 string) {
		m := Manifest{Items: []Item{file(p1, 1), file(p2, 1)}}
		if m.Validate(Default) != nil {
			return
		}
		checkSafe(t, m)
	})
}

func checkSafe(t *testing.T, m Manifest) {
	t.Helper()
	keys := map[string]bool{}
	for _, it := range m.Items {
		joined := path.Join("/dest", it.Path)
		if !strings.HasPrefix(joined, "/dest/") {
			t.Fatalf("accepted %q, which escapes to %q", it.Path, joined)
		}
		k := foldKey(it.Path)
		if keys[k] {
			t.Fatalf("accepted two items folding to %q", k)
		}
		keys[k] = true
	}
}
