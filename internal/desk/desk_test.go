//go:build !js

package desk

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/richardwooding/parley/relay"

	"github.com/richardwooding/satchel/internal/clip"
	"github.com/richardwooding/satchel/internal/manifest"
	"github.com/richardwooding/satchel/internal/share"
	"github.com/richardwooding/satchel/internal/xfer"
)

type fakeClip struct {
	mu   sync.Mutex
	text string
	png  []byte
}

func (f *fakeClip) Read() (clip.Content, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clip.Content{Text: f.text, PNG: f.png}, nil
}
func (f *fakeClip) WriteText(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.text, f.png = s, nil
	return nil
}
func (f *fakeClip) WritePNG(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.png, f.text = b, ""
	return nil
}

type rig struct {
	d     *Desk
	clip  *fakeClip
	dl    string
	notes chan Note
}

func relayURL(t *testing.T) string {
	t.Helper()
	s := relay.New(relay.Options{Grace: 5 * time.Second, ConnRate: rate.Inf})
	t.Cleanup(s.Close)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func newRig(t *testing.T, url string) *rig {
	t.Helper()
	r := &rig{clip: &fakeClip{}, dl: t.TempDir(), notes: make(chan Note, 64)}
	r.d = New(Config{RelayURL: url, Downloads: r.dl, Clipboard: r.clip, Notify: func(n Note) { r.notes <- n }})
	t.Cleanup(r.d.Close)
	return r
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

func (r *rig) note(t *testing.T, kind string) Note {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case n := <-r.notes:
			if n.Kind == "failed" && kind != "failed" {
				t.Fatalf("failed: %s: %s", n.Title, n.Body)
			}
			if n.Kind == kind {
				return n
			}
		case <-deadline:
			t.Fatalf("no %q notification", kind)
		}
	}
}

func TestClipboardTextArrivesOnTheOtherClipboard(t *testing.T) {
	url := relayURL(t)
	a, b := newRig(t, url), newRig(t, url)
	a.clip.text = "meet at 7 · ünïcødé"

	phrase, err := a.d.ShareClipboard(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	a.note(t, "share-ready")
	if err := b.d.Receive(ctx(t), "  "+strings.ToUpper(phrase)+" "); err != nil {
		t.Fatal(err)
	}
	b.note(t, "received")
	if got, _ := b.clip.Read(); got.Text != "meet at 7 · ünïcødé" {
		t.Fatalf("receiver clipboard %q", got.Text)
	}
	st := b.d.State()
	if len(st.Receives) != 1 || len(st.Receives[0].Offers) != 1 || st.Receives[0].Offers[0].Text == "" {
		t.Fatalf("state %+v", st)
	}
	waitFor(t, func() bool { s := a.d.State(); return len(s.Shares) == 1 && s.Shares[0].Done == 1 })
}

func TestClipboardImageIsCopiedAndSaved(t *testing.T) {
	url := relayURL(t)
	a, b := newRig(t, url), newRig(t, url)
	a.clip.png = bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 50_000) // 200 KB: pulled, not inlined

	phrase, err := a.d.ShareClipboard(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.d.Receive(ctx(t), phrase); err != nil {
		t.Fatal(err)
	}
	n := b.note(t, "received")
	if got, _ := b.clip.Read(); !bytes.Equal(got.PNG, a.clip.png) {
		t.Fatalf("receiver clipboard has %d image bytes", len(got.PNG))
	}
	saved, _ := filepath.Glob(filepath.Join(b.dl, "image-*.png"))
	if len(saved) != 1 || !strings.Contains(n.Body, "saved as") {
		t.Fatalf("saved %v, note %q", saved, n.Body)
	}
}

func TestFolderLandsInDownloads(t *testing.T) {
	url := relayURL(t)
	a, b := newRig(t, url), newRig(t, url)
	src := t.TempDir()
	for p, data := range map[string]string{"trip/day1/a.jpg": "aaa", "trip/day2/b.jpg": "bbbb", "trip/notes.md": "# hi"} {
		full := filepath.Join(src, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(data), 0o644)
	}
	// Something already called "trip" in the receiver's downloads stays put.
	_ = os.WriteFile(filepath.Join(b.dl, "trip"), []byte("mine"), 0o644)

	phrase, err := a.d.ShareFiles(ctx(t), []string{filepath.Join(src, "trip")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.d.Receive(ctx(t), phrase); err != nil {
		t.Fatal(err)
	}
	n := b.note(t, "received")
	if n.Folder != b.dl {
		t.Fatalf("note folder %q", n.Folder)
	}
	if got, _ := os.ReadFile(filepath.Join(b.dl, "trip (1)", "day2", "b.jpg")); string(got) != "bbbb" {
		t.Fatalf("b.jpg = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(b.dl, "trip")); string(got) != "mine" {
		t.Fatal("existing file overwritten")
	}
}

// A phone opens the page and sends to a phrase the tray opened for it.
func TestOpenReceivesFromBrowserSender(t *testing.T) {
	url := relayURL(t)
	b := newRig(t, url)
	phrase, err := b.d.Open(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	phone, err := share.Join(ctx(t), url, phrase)
	if err != nil {
		t.Fatal(err)
	}
	go phone.Run(func(any) {})
	t.Cleanup(phone.Close)
	text := []byte("from my phone")
	if _, err := phone.X.Offer(manifest.Manifest{Items: []manifest.Item{
		xfer.ItemFor("t.txt", manifest.KindText, "", text),
	}}, xfer.Bytes{text}); err != nil {
		t.Fatal(err)
	}
	b.note(t, "received")
	if got, _ := b.clip.Read(); got.Text != "from my phone" {
		t.Fatalf("got %q", got.Text)
	}
}

func TestStopAndLink(t *testing.T) {
	url := relayURL(t)
	a := newRig(t, url)
	a.clip.text = "x"
	phrase, err := a.d.ShareClipboard(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if l := a.d.State().Shares[0].Link; !strings.HasSuffix(l, "/#"+phrase) || !strings.HasPrefix(l, "http://127.0.0.1:") {
		t.Fatalf("link %q", l)
	}
	a.d.Stop(phrase)
	if n := len(a.d.State().Shares); n != 0 {
		t.Fatalf("%d shares after stop", n)
	}
	if err := newRig(t, url).d.Receive(ctx(t), phrase); err == nil {
		t.Fatal("joined a stopped share")
	}
	if New(Config{}).Link("a-1-b") != "https://satchel-send.fly.dev/#a-1-b" {
		t.Fatal("default link")
	}
}

func TestEmptyClipboard(t *testing.T) {
	a := newRig(t, relayURL(t))
	if _, err := a.d.ShareClipboard(ctx(t)); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("got %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
