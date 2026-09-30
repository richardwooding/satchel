//go:build !js

package sink

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/richardwooding/satchel/internal/manifest"
)

// Dir writes items beneath a destination directory through an os.Root, so no
// path from a peer can reach outside it — not with "..", not through a
// symlink planted in the destination, whatever manifest validation missed.
//
// It never overwrites: a top-level name that already exists is given a free
// " (n)" variant, and everything beneath it moves with it.
type Dir struct {
	root     *os.Root
	manifest manifest.Manifest
	rename   map[string]string // top-level name → name actually used
	created  []string          // committed top-level names, for Placed
}

// NewDir opens (creating if needed) dest as the jail for one offer.
func NewDir(dest string) (*Dir, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return nil, err
	}
	return &Dir{root: root}, nil
}

func (d *Dir) Prepare(m manifest.Manifest) error {
	d.manifest = m
	d.rename = map[string]string{}
	for _, it := range m.Items {
		top, _, _ := strings.Cut(it.Path, "/")
		if _, ok := d.rename[top]; ok {
			continue
		}
		free, err := d.freeName(top)
		if err != nil {
			return err
		}
		d.rename[top] = free
	}
	return nil
}

// freeName returns name, or "name (n).ext" for the smallest n not already
// present and not already handed to another top-level item of this offer.
func (d *Dir) freeName(name string) (string, error) {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // ".bashrc": the whole name is the stem
		stem, ext = name, ""
	}
	for n := range 10_000 {
		cand := name
		if n > 0 {
			cand = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		}
		if d.taken(cand) {
			continue
		}
		_, err := d.root.Lstat(cand)
		if errors.Is(err, fs.ErrNotExist) {
			return cand, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("sink: no free name for %q", name)
}

func (d *Dir) taken(name string) bool {
	for _, v := range d.rename {
		if strings.EqualFold(v, name) {
			return true
		}
	}
	return false
}

// local maps a manifest path to where it is written.
func (d *Dir) local(p string) string {
	top, rest, found := strings.Cut(p, "/")
	top = d.rename[top]
	if !found {
		return top
	}
	return top + "/" + rest
}

func (d *Dir) Mkdir(i int) error {
	p := d.local(d.manifest.Items[i].Path)
	if err := d.root.MkdirAll(p, 0o755); err != nil {
		return err
	}
	d.noteTop(p)
	return nil
}

func (d *Dir) Create(i int) (ItemWriter, error) {
	final := d.local(d.manifest.Items[i].Path)
	if dir := path.Dir(final); dir != "." {
		if err := d.root.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	tmp := path.Join(path.Dir(final), ".satchel-"+hex.EncodeToString(b[:])+".part")
	f, err := d.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	return &dirWriter{d: d, f: f, tmp: tmp, final: final}, nil
}

func (d *Dir) noteTop(p string) {
	top, _, _ := strings.Cut(p, "/")
	if slices.Contains(d.created, top) {
		return
	}
	d.created = append(d.created, top)
}

// Placed lists the top-level names written, after any renaming, in the
// order they were first committed.
func (d *Dir) Placed() []string { return append([]string(nil), d.created...) }

// Root is the destination directory.
func (d *Dir) Root() string { return d.root.Name() }

func (d *Dir) Finish() error  { return d.root.Close() }
func (d *Dir) Abandon() error { return d.root.Close() }

type dirWriter struct {
	d          *Dir
	f          *os.File
	tmp, final string
}

func (w *dirWriter) Write(p []byte) (int, error) { return w.f.Write(p) }

func (w *dirWriter) Commit() error {
	if err := w.f.Sync(); err != nil {
		_ = w.Abort()
		return err
	}
	if err := w.f.Close(); err != nil {
		_ = w.d.root.Remove(w.tmp)
		return err
	}
	// The final name was free when Prepare chose it; refuse rather than
	// clobber something that appeared since.
	if _, err := w.d.root.Lstat(w.final); !errors.Is(err, fs.ErrNotExist) {
		_ = w.d.root.Remove(w.tmp)
		return fmt.Errorf("sink: %q appeared during the transfer; not overwriting", w.final)
	}
	if err := w.d.root.Rename(w.tmp, w.final); err != nil {
		_ = w.d.root.Remove(w.tmp)
		return err
	}
	w.d.noteTop(w.final)
	return nil
}

func (w *dirWriter) Abort() error {
	_ = w.f.Close()
	return w.d.root.Remove(w.tmp)
}
