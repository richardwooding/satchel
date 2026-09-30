//go:build !js

package source

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/richardwooding/satchel/internal/manifest"
)

func write(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFolderAndFile(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "album", "a.jpg"), "aaaa")
	write(t, filepath.Join(root, "album", "raw", "b.cr3"), "bb")
	write(t, filepath.Join(root, "notes.txt"), "hello")
	secret := filepath.Join(t.TempDir(), "secret")
	write(t, secret, "do not share")
	if err := os.Symlink(secret, filepath.Join(root, "album", "link")); err != nil {
		t.Fatal(err)
	}

	var last int64
	b, err := Build([]string{filepath.Join(root, "album"), filepath.Join(root, "notes.txt")}, func(done, total int64, _ string) {
		last = done
		if done > total {
			t.Errorf("progress %d past total %d", done, total)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Files.Close() }()

	got := map[string]manifest.Item{}
	for _, it := range b.Manifest.Items {
		got[it.Path] = it
	}
	for _, want := range []string{"album", "album/a.jpg", "album/raw", "album/raw/b.cr3", "notes.txt"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s in %v", want, b.Manifest.Items)
		}
	}
	if _, ok := got["album/link"]; ok {
		t.Fatal("followed a symlink out of the picked folder")
	}
	if len(b.Skipped) != 1 || filepath.Base(b.Skipped[0]) != "link" {
		t.Fatalf("skipped %v", b.Skipped)
	}
	if last != 11 {
		t.Fatalf("hashed %d bytes, want 11", last)
	}
	sum := sha256.Sum256([]byte("hello"))
	if !bytes.Equal(got["notes.txt"].SHA256, sum[:]) || got["album/a.jpg"].Mime != "image/jpeg" {
		t.Fatalf("notes.txt %+v", got["notes.txt"])
	}

	for i, it := range b.Manifest.Items {
		if it.Path != "album/raw/b.cr3" {
			continue
		}
		buf := make([]byte, 2)
		if n, err := b.Files.ReadAt(i, buf, 0); n != 2 || string(buf) != "bb" {
			t.Fatalf("ReadAt: %q %v", buf, err)
		}
	}
}

// Picking two things with the same name would collide at the receiver, so it
// fails here, before anything is offered.
func TestBuildRejectsCollidingPicks(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write(t, filepath.Join(a, "report.pdf"), "1")
	write(t, filepath.Join(b, "Report.PDF"), "2")
	if _, err := Build([]string{filepath.Join(a, "report.pdf"), filepath.Join(b, "Report.PDF")}, nil); err == nil {
		t.Fatal("colliding picks accepted")
	}
}

func TestBuildOnlySymlinkIsNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t")
	write(t, target, "x")
	link := filepath.Join(dir, "l")
	_ = os.Symlink(target, link)
	if _, err := Build([]string{link}, nil); err == nil {
		t.Fatal("a lone symlink produced an offer")
	}
}
