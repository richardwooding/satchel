//go:build !js

package sink

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/richardwooding/satchel/internal/manifest"
)

var digest = make([]byte, 32)

func write(t *testing.T, s Sink, i int, data string) {
	t.Helper()
	w, err := s.Create(i)
	if err != nil {
		t.Fatalf("create %d: %v", i, err)
	}
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("commit %d: %v", i, err)
	}
}

func TestDirWritesTree(t *testing.T) {
	dest := t.TempDir()
	d, err := NewDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	m := manifest.Manifest{Items: []manifest.Item{
		{Path: "album", Kind: manifest.KindDir},
		{Path: "album/a.txt", Kind: manifest.KindFile, Size: 5, SHA256: digest},
		{Path: "album/deep/b.txt", Kind: manifest.KindFile, Size: 3, SHA256: digest},
	}}
	if err := d.Prepare(m); err != nil {
		t.Fatal(err)
	}
	if err := d.Mkdir(0); err != nil {
		t.Fatal(err)
	}
	write(t, d, 1, "hello")
	write(t, d, 2, "abc")
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"album/a.txt": "hello", "album/deep/b.txt": "abc"} {
		got, err := os.ReadFile(filepath.Join(dest, p))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", p, got, err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dest, "album", ".satchel-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
	if !slices.Equal(d.Placed(), []string{"album"}) {
		t.Fatalf("placed %v", d.Placed())
	}
}

func TestDirNeverOverwrites(t *testing.T) {
	dest := t.TempDir()
	for _, name := range []string{"photo.png", "photo (1).png", "notes"} {
		if err := os.WriteFile(filepath.Join(dest, name), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d, _ := NewDir(dest)
	m := manifest.Manifest{Items: []manifest.Item{
		{Path: "photo.png", Kind: manifest.KindFile, Size: 5, SHA256: digest},
		{Path: "notes/x", Kind: manifest.KindFile, Size: 1, SHA256: digest},
	}}
	if err := d.Prepare(m); err != nil {
		t.Fatal(err)
	}
	write(t, d, 0, "their")
	write(t, d, 1, "y")
	_ = d.Finish()

	if b, _ := os.ReadFile(filepath.Join(dest, "photo.png")); string(b) != "mine" {
		t.Fatal("existing file was overwritten")
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "photo (2).png")); string(b) != "their" {
		t.Fatalf("incoming file not at photo (2).png: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "notes (1)", "x")); string(b) != "y" {
		t.Fatalf("incoming folder not renamed as a unit: %q", b)
	}
	if !slices.Equal(d.Placed(), []string{"photo (2).png", "notes (1)"}) {
		t.Fatalf("placed %v", d.Placed())
	}
}

// A symlink that appears after names were chosen must not carry a write
// outside the destination. Validation cannot see this; only the jail can.
func TestDirSymlinkRaceStaysJailed(t *testing.T) {
	dest, outside := t.TempDir(), t.TempDir()
	d, _ := NewDir(dest)
	m := manifest.Manifest{Items: []manifest.Item{
		{Path: "box/payload", Kind: manifest.KindFile, Size: 3, SHA256: digest},
	}}
	if err := d.Prepare(m); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "box")); err != nil {
		t.Fatal(err)
	}
	w, err := d.Create(0)
	if err == nil {
		_, _ = w.Write([]byte("pwn"))
		_ = w.Commit()
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("write escaped the jail: %v", entries)
	}
	if err == nil {
		t.Fatal("create through a planted symlink succeeded")
	}
	t.Logf("refused: %v", err)
}

func TestDirAbortLeavesNothing(t *testing.T) {
	dest := t.TempDir()
	d, _ := NewDir(dest)
	_ = d.Prepare(manifest.Manifest{Items: []manifest.Item{{Path: "a", Kind: manifest.KindFile, Size: 9, SHA256: digest}}})
	w, err := d.Create(0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("partial"))
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
	_ = d.Abandon()
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("abort left %v", entries)
	}
}

func TestMemoryCap(t *testing.T) {
	m := NewMemory(4)
	if err := m.Prepare(manifest.Manifest{Items: []manifest.Item{{Path: "a", Kind: manifest.KindText, Size: 9, SHA256: digest}}}); err == nil {
		t.Fatal("memory sink accepted an offer over its cap")
	}
	_ = m.Prepare(manifest.Manifest{Items: []manifest.Item{{Path: "a", Kind: manifest.KindText, Size: 3, SHA256: digest}}})
	w, _ := m.Create(0)
	// A peer that lies about the size is still stopped at the cap.
	if _, err := w.Write([]byte("12345")); err == nil {
		t.Fatal("memory sink wrote past its cap")
	}
}
