//go:build js && wasm

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall/js"

	"github.com/richardwooding/satchel/internal/manifest"
)

// hashSlice is how much of a File is read per await while hashing.
const hashSlice = 4 << 20

// fileSource serves an offer's items straight from the File objects the
// person picked; nothing is copied into Go memory ahead of a pull.
type fileSource []js.Value

func (f fileSource) ReadAt(item int, p []byte, off int64) (int, error) {
	if item < 0 || item >= len(f) {
		return 0, fmt.Errorf("no item %d", item)
	}
	size := int64(f[item].Get("size").Float())
	if off >= size {
		return 0, io.EOF
	}
	end := min(size, off+int64(len(p)))
	n, err := readSlice(f[item], off, end, p)
	if err == nil && end == size && n < len(p) {
		err = io.EOF
	}
	return n, err
}

// readSlice copies bytes [off, end) of a Blob into p.
func readSlice(blob js.Value, off, end int64, p []byte) (int, error) {
	buf, err := await(blob.Call("slice", off, end).Call("arrayBuffer"))
	if err != nil {
		return 0, err
	}
	return js.CopyBytesToGo(p, js.Global().Get("Uint8Array").New(buf)), nil
}

// offerFiles builds a manifest from picked files — webkitRelativePath keeps
// a picked folder's structure — hashes each one, and offers them.
func offerFiles(files js.Value) {
	s := live()
	if s == nil {
		emitError("start or join a session first")
		return
	}
	n := files.Length()
	if files.IsUndefined() || n == 0 {
		emitError("no files chosen")
		return
	}
	var items []manifest.Item
	var src fileSource
	for i := range n {
		f := files.Index(i)
		it, err := describe(f, i, n)
		if err != nil {
			emitError(err.Error())
			return
		}
		items = append(items, it)
		src = append(src, f)
	}
	m := manifest.Manifest{Items: items}
	if err := m.Validate(manifest.Default); err != nil {
		emitError("can't send these: " + strings.TrimPrefix(err.Error(), manifest.ErrInvalid.Error()+": "))
		return
	}
	id, err := s.X.Offer(m, src)
	if err != nil {
		emitError(err.Error())
		return
	}
	emit("offered", map[string]any{"id": id.String(), "name": items[0].Path, "items": len(items), "bytes": m.Total()})
}

func describe(f js.Value, i, n int) (manifest.Item, error) {
	p := f.Get("webkitRelativePath").String()
	if f.Get("webkitRelativePath").IsUndefined() || p == "" {
		p = f.Get("name").String()
	}
	size := int64(f.Get("size").Float())
	emit("hashing", map[string]any{"index": i, "count": n, "name": p})
	h := sha256.New()
	buf := make([]byte, hashSlice)
	for off := int64(0); off < size; off += hashSlice {
		end := min(size, off+hashSlice)
		k, err := readSlice(f, off, end, buf)
		if err != nil {
			return manifest.Item{}, fmt.Errorf("couldn't read %s: %w", p, err)
		}
		h.Write(buf[:k])
	}
	kind := manifest.KindFile
	mime := f.Get("type").String()
	if strings.HasPrefix(mime, "image/") && n == 1 {
		kind = manifest.KindImage
	}
	return manifest.Item{Path: p, Kind: kind, Size: size, SHA256: h.Sum(nil), Mime: mime}, nil
}

// await blocks the calling goroutine (never the JS event loop) on a promise.
func await(promise js.Value) (js.Value, error) {
	ok := make(chan js.Value, 1)
	bad := make(chan error, 1)
	then := js.FuncOf(func(_ js.Value, args []js.Value) any { ok <- args[0]; return nil })
	catch := js.FuncOf(func(_ js.Value, args []js.Value) any {
		bad <- errors.New(args[0].Call("toString").String())
		return nil
	})
	defer then.Release()
	defer catch.Release()
	promise.Call("then", then).Call("catch", catch)
	select {
	case v := <-ok:
		return v, nil
	case err := <-bad:
		return js.Undefined(), err
	}
}
