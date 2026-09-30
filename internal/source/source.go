//go:build !js

// Package source turns files and folders a person picked into an offer: a
// manifest plus an xfer.Source that reads them from disk on demand.
//
// Each picked path becomes a top-level item named by its base name; a folder
// brings its tree beneath it. Symlinks are skipped, not followed — the
// manifest has no symlink kind, and following one could share something
// outside what was picked. Anything else that is not a regular file or a
// directory (sockets, devices) is skipped too. What was skipped is reported.
package source

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/richardwooding/satchel/internal/manifest"
)

// Files is an offer's items on disk, indexed like its manifest.
type Files struct {
	paths []string // absolute path per item; "" for a directory

	mu   sync.Mutex
	open map[int]*os.File
}

// Progress reports hashing as it goes, in bytes.
type Progress func(done, total int64, current string)

// Built is the result of Build.
type Built struct {
	Manifest manifest.Manifest
	Files    *Files
	Skipped  []string // symlinks and special files that were left out
}

type entry struct {
	rel, abs string
	kind     manifest.Kind
	size     int64
}

// Build walks the picked paths, hashes every file, and returns the offer.
func Build(picked []string, progress Progress) (Built, error) {
	if len(picked) == 0 {
		return Built{}, errors.New("source: nothing picked")
	}
	var entries []entry
	var skipped []string
	for _, p := range picked {
		es, sk, err := walk(p)
		if err != nil {
			return Built{}, err
		}
		entries = append(entries, es...)
		skipped = append(skipped, sk...)
	}
	if len(entries) == 0 {
		return Built{}, fmt.Errorf("source: nothing shareable in %s", strings.Join(picked, ", "))
	}
	var total int64
	for _, e := range entries {
		total += e.size
	}
	b := Built{Files: &Files{open: map[int]*os.File{}}, Skipped: skipped}
	var done int64
	for _, e := range entries {
		it := manifest.Item{Path: e.rel, Kind: e.kind, Size: e.size}
		if e.kind == manifest.KindFile {
			sum, err := hashFile(e.abs, func(n int64) {
				if progress != nil {
					progress(done+n, total, e.rel)
				}
			})
			if err != nil {
				return Built{}, err
			}
			it.SHA256 = sum
			it.Mime = mimeFor(e.rel)
			done += e.size
		}
		b.Manifest.Items = append(b.Manifest.Items, it)
		b.Files.paths = append(b.Files.paths, e.abs)
	}
	if err := b.Manifest.Validate(manifest.Default); err != nil {
		return Built{}, err
	}
	return b, nil
}

// walk lists one picked path as manifest entries under its base name.
func walk(picked string) ([]entry, []string, error) {
	abs, err := filepath.Abs(picked)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, nil, err
	}
	base := filepath.Base(abs)
	switch {
	case info.Mode().IsRegular():
		return []entry{{rel: base, abs: abs, kind: manifest.KindFile, size: info.Size()}}, nil, nil
	case !info.IsDir():
		return nil, []string{abs}, nil
	}
	var entries []entry
	var skipped []string
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return err
		}
		rel = path.Join(base, filepath.ToSlash(rel))
		switch t := d.Type(); {
		case t.IsDir():
			entries = append(entries, entry{rel: rel, abs: p, kind: manifest.KindDir})
		case t.IsRegular():
			fi, err := d.Info()
			if err != nil {
				return err
			}
			entries = append(entries, entry{rel: rel, abs: p, kind: manifest.KindFile, size: fi.Size()})
		default: // symlink, socket, device, pipe
			skipped = append(skipped, p)
		}
		return nil
	})
	return entries, skipped, err
}

func hashFile(p string, progress func(int64)) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var n int64
	for {
		k, err := f.Read(buf)
		h.Write(buf[:k])
		n += int64(k)
		progress(n)
		if errors.Is(err, io.EOF) {
			return h.Sum(nil), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// ReadAt implements xfer.Source. Files stay open once read, since a pull is
// usually followed by the next chunk of the same file.
func (f *Files) ReadAt(item int, p []byte, off int64) (int, error) {
	file, err := f.file(item)
	if err != nil {
		return 0, err
	}
	return file.ReadAt(p, off)
}

func (f *Files) file(item int) (*os.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if file, ok := f.open[item]; ok {
		return file, nil
	}
	if item < 0 || item >= len(f.paths) {
		return nil, fmt.Errorf("source: no item %d", item)
	}
	file, err := os.Open(f.paths[item])
	if err != nil {
		return nil, err
	}
	f.open[item] = file
	return file, nil
}

// Close releases every open file.
func (f *Files) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var errs []error
	for i, file := range f.open {
		errs = append(errs, file.Close())
		delete(f.open, i)
	}
	return errors.Join(errs...)
}

// mimeFor guesses from the extension alone; the receiver treats it as a
// hint for display, never as a reason to trust the content.
func mimeFor(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".txt", ".md":
		return "text/plain"
	case ".pdf":
		return "application/pdf"
	}
	return ""
}
