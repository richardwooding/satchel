//go:build !js

// Package clip reads and writes the system clipboard, images included.
//
// Wails' own clipboard is text only, so images go through the platform's
// tools. On Wayland that is wl-clipboard (wl-copy/wl-paste), measured to
// round-trip a PNG byte-identically; X11 uses xclip when it is installed.
// Elsewhere only text is available, through the fallback the caller supplies.
package clip

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Content is one clipboard reading: text, or an image as PNG.
type Content struct {
	Text string
	PNG  []byte
}

// Empty reports whether there is nothing to share.
func (c Content) Empty() bool { return strings.TrimSpace(c.Text) == "" && len(c.PNG) == 0 }

// TextFallback is the text-only clipboard used when no image-capable tool
// exists — the Wails clipboard, in the app.
type TextFallback interface {
	Text() (string, bool)
	SetText(string) bool
}

// Clipboard picks a backend once, at construction.
type Clipboard struct {
	tool     backend
	fallback TextFallback
}

// ErrUnavailable means the clipboard could not be read or written. Without
// wl-clipboard on Wayland that is expected whenever satchel's window is not
// focused, so the message says what to install.
var ErrUnavailable = errors.New("clipboard unavailable — install wl-clipboard (Wayland) or xclip (X11)")

type backend interface {
	types(ctx context.Context) ([]string, error)
	read(ctx context.Context, mime string) ([]byte, error)
	write(ctx context.Context, mime string, data []byte) error
}

// New chooses wl-clipboard under Wayland, xclip under X11, else text only.
func New(fallback TextFallback) *Clipboard {
	c := &Clipboard{fallback: fallback}
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "" && have("wl-paste") && have("wl-copy"):
		c.tool = wayland{}
	case os.Getenv("DISPLAY") != "" && have("xclip"):
		c.tool = xclip{}
	}
	return c
}

// Images reports whether this clipboard can carry images.
func (c *Clipboard) Images() bool { return c.tool != nil }

// Read returns an image if the clipboard holds one, else its text.
func (c *Clipboard) Read() (Content, error) {
	if c.tool == nil {
		t, ok := c.fallbackText()
		if !ok {
			return Content{}, ErrUnavailable
		}
		return Content{Text: t}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	types, err := c.tool.types(ctx)
	if err != nil {
		return Content{}, err
	}
	if slices.Contains(types, "image/png") {
		png, err := c.tool.read(ctx, "image/png")
		if err == nil && len(png) > 0 {
			return Content{PNG: png}, nil
		}
	}
	for _, t := range []string{"text/plain;charset=utf-8", "UTF8_STRING", "text/plain", "STRING"} {
		if slices.Contains(types, t) {
			b, err := c.tool.read(ctx, t)
			if err != nil {
				return Content{}, err
			}
			return Content{Text: string(b)}, nil
		}
	}
	return Content{}, nil
}

// WriteText puts text on the clipboard.
func (c *Clipboard) WriteText(s string) error {
	if c.tool == nil {
		if c.fallback != nil && c.fallback.SetText(s) {
			return nil
		}
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return c.tool.write(ctx, "text/plain;charset=utf-8", []byte(s))
}

// WritePNG puts an image on the clipboard.
func (c *Clipboard) WritePNG(png []byte) error {
	if c.tool == nil {
		return ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return c.tool.write(ctx, "image/png", png)
}

// fallbackText reads through the text-only fallback, giving up after a few
// seconds. The GTK clipboard can block indefinitely: on Wayland only a
// focused window may read the selection, and a tray app usually has none.
func (c *Clipboard) fallbackText() (string, bool) {
	if c.fallback == nil {
		return "", false
	}
	type result struct {
		s  string
		ok bool
	}
	ch := make(chan result, 1)
	go func() {
		s, ok := c.fallback.Text()
		ch <- result{s, ok}
	}()
	select {
	case r := <-ch:
		return r.s, r.ok
	case <-time.After(fallbackTimeout):
		return "", false
	}
}

// fallbackTimeout bounds a read through the text-only fallback.
var fallbackTimeout = 3 * time.Second

func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

type wayland struct{}

func (wayland) types(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "wl-paste", "--list-types").Output()
	if err != nil {
		// An empty clipboard is reported as an error by wl-paste.
		return nil, nil
	}
	return strings.Fields(string(out)), nil
}

func (wayland) read(ctx context.Context, mime string) ([]byte, error) {
	return exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", mime).Output()
}

// write hands the data to wl-copy, which forks a small process that keeps
// serving the selection after we return — Wayland has no clipboard manager
// to hand it to otherwise.
func (wayland) write(ctx context.Context, mime string, data []byte) error {
	cmd := exec.CommandContext(ctx, "wl-copy", "--type", mime)
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}

type xclip struct{}

func (xclip) types(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
	if err != nil {
		return nil, nil
	}
	return strings.Fields(string(out)), nil
}

func (xclip) read(ctx context.Context, mime string) ([]byte, error) {
	return exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", mime, "-o").Output()
}

func (xclip) write(ctx context.Context, mime string, data []byte) error {
	cmd := exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", mime, "-i")
	cmd.Stdin = bytes.NewReader(data)
	return cmd.Run()
}
