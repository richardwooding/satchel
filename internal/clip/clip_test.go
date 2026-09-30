//go:build !js

package clip

import (
	"bytes"
	"os"
	"testing"
	"time"
)

// These tests replace the real clipboard, so they run only when asked:
//
//	SATCHEL_CLIPBOARD_TEST=1 go test ./internal/clip
//
// and restore whatever text was there afterwards.
func liveClipboard(t *testing.T) *Clipboard {
	t.Helper()
	if os.Getenv("SATCHEL_CLIPBOARD_TEST") == "" {
		t.Skip("set SATCHEL_CLIPBOARD_TEST=1 to exercise the real clipboard")
	}
	c := New(nil)
	if !c.Images() {
		t.Skip("no wl-clipboard or xclip here")
	}
	if prev, err := c.Read(); err == nil && prev.Text != "" {
		t.Cleanup(func() { _ = c.WriteText(prev.Text) })
	}
	return c
}

func TestTextRoundTrip(t *testing.T) {
	c := liveClipboard(t)
	want := "satchel clip test · ünïcødé\nline two"
	if err := c.WriteText(want); err != nil {
		t.Fatal(err)
	}
	got, err := c.Read()
	if err != nil || got.Text != want {
		t.Fatalf("read back %q, %v", got.Text, err)
	}
}

func TestImageRoundTrip(t *testing.T) {
	c := liveClipboard(t)
	// The smallest valid PNG: 1x1, one grey pixel.
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0, 0, 0, 1, 0, 0, 0, 1, 8, 0, 0, 0, 0, 0x3a, 0x7e, 0x9b, 0x55, 0, 0, 0, 0x0a, 0x49, 0x44,
		0x41, 0x54, 0x78, 0x9c, 0x63, 0x68, 0, 0, 0, 0x82, 0, 0x81, 0x4c, 0x17, 0xd7, 0xdf, 0, 0, 0,
		0, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	if err := c.WritePNG(png); err != nil {
		t.Fatal(err)
	}
	got, err := c.Read()
	if err != nil || !bytes.Equal(got.PNG, png) {
		t.Fatalf("read back %d bytes (want %d), %v", len(got.PNG), len(png), err)
	}
}

type fakeText struct{ s string }

func (f *fakeText) Text() (string, bool)  { return f.s, true }
func (f *fakeText) SetText(s string) bool { f.s = s; return true }

func TestFallbackIsTextOnly(t *testing.T) {
	f := &fakeText{s: "hi"}
	c := &Clipboard{fallback: f}
	if c.Images() {
		t.Fatal("fallback claims image support")
	}
	if got, err := c.Read(); err != nil || got.Text != "hi" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if err := c.WritePNG([]byte{1}); err == nil {
		t.Fatal("fallback accepted an image")
	}
	_ = c.WriteText("there")
	if f.s != "there" {
		t.Fatal("fallback write lost")
	}
}

type hangingText struct{}

func (hangingText) Text() (string, bool) { select {} }
func (hangingText) SetText(string) bool  { return false }

// A fallback that never answers must not hang a share.
func TestFallbackReadTimesOut(t *testing.T) {
	old := fallbackTimeout
	fallbackTimeout = 50 * time.Millisecond
	t.Cleanup(func() { fallbackTimeout = old })
	c := &Clipboard{fallback: hangingText{}}
	start := time.Now()
	if _, err := c.Read(); err == nil {
		t.Fatal("hung fallback reported success")
	}
	if time.Since(start) > time.Second {
		t.Fatal("read waited for the hung fallback")
	}
}
