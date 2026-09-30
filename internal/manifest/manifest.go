// Package manifest describes what an offer contains and decides whether a
// manifest from a peer is safe to act on. It is the boundary where input from
// the other end of a session first meets the local filesystem, so everything
// here assumes the peer is hostile: every path is checked before anything is
// created, and the receive-side jail (internal/sink) stays the defence even if
// a check here is wrong.
package manifest

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/richardwooding/parley/wire"
)

// Kind says what an item is. Text and Image are clipboard contents; File and
// Dir come from the filesystem.
type Kind uint8

const (
	KindText  Kind = 1
	KindImage Kind = 2
	KindFile  Kind = 3
	KindDir   Kind = 4
)

func (k Kind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindImage:
		return "image"
	case KindFile:
		return "file"
	case KindDir:
		return "dir"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// Item is one entry. Path is always slash-separated and relative to the
// receiver's destination directory, whatever OS either end runs.
type Item struct {
	Path   string `cbor:"1,keyasint"`
	Kind   Kind   `cbor:"2,keyasint"`
	Size   int64  `cbor:"3,keyasint,omitempty"`
	SHA256 []byte `cbor:"4,keyasint,omitempty"`
	Mime   string `cbor:"5,keyasint,omitempty"`
}

// Manifest is the full list of items in an offer.
type Manifest struct {
	Items []Item `cbor:"1,keyasint"`
}

// Limits bound what a receiver will accept. The zero value is not useful;
// start from Default.
type Limits struct {
	MaxItems   int
	MaxBytes   int64 // sum of item sizes
	MaxPath    int   // bytes, per item path
	MaxDepth   int   // path components
	MaxMimeLen int
}

// Default suits a desktop receiver writing to disk. A browser receiver holds
// everything in memory and should pass something far smaller.
var Default = Limits{
	MaxItems:   100_000,
	MaxBytes:   1 << 40, // 1 TiB
	MaxPath:    1024,
	MaxDepth:   64,
	MaxMimeLen: 127,
}

// Total is the sum of item sizes.
func (m Manifest) Total() int64 {
	var n int64
	for _, it := range m.Items {
		n += it.Size
	}
	return n
}

// Encode serializes a manifest in the wire's CBOR profile.
func (m Manifest) Encode() ([]byte, error) { return wire.Marshal(m) }

// Decode parses and validates a manifest. It never returns a manifest that
// failed validation.
func Decode(b []byte, l Limits) (Manifest, error) {
	m, err := wire.Body[Manifest](b)
	if err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if err := m.Validate(l); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("manifest: invalid")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Validate reports whether m is safe to materialize under a destination
// directory. It rejects anything that could name a location outside that
// directory, two items that would land on the same file (including on
// case-insensitive or Unicode-normalizing filesystems), and a file that
// another item treats as a directory.
func (m Manifest) Validate(l Limits) error {
	if len(m.Items) == 0 {
		return invalid("no items")
	}
	if len(m.Items) > l.MaxItems {
		return invalid("%d items exceeds limit %d", len(m.Items), l.MaxItems)
	}
	seen := make(map[string]Kind, len(m.Items))
	var total int64
	for i, it := range m.Items {
		if err := it.validate(l); err != nil {
			return fmt.Errorf("item %d: %w", i, err)
		}
		if it.Size > l.MaxBytes-total {
			return invalid("total size exceeds limit %d", l.MaxBytes)
		}
		total += it.Size
		key := foldKey(it.Path)
		if _, dup := seen[key]; dup {
			return invalid("item %d: %q collides with an earlier item", i, it.Path)
		}
		seen[key] = it.Kind
	}
	// A path's ancestors must be directories if they are listed at all:
	// "a" as a file plus "a/b" would need "a" to be both.
	for _, it := range m.Items {
		for dir := path.Dir(it.Path); dir != "."; dir = path.Dir(dir) {
			if k, ok := seen[foldKey(dir)]; ok && k != KindDir {
				return invalid("%q is inside %q, which is not a directory", it.Path, dir)
			}
		}
	}
	return nil
}

func (it Item) validate(l Limits) error {
	switch it.Kind {
	case KindText, KindImage, KindFile:
		if it.Size < 0 {
			return invalid("negative size")
		}
		if len(it.SHA256) != 32 {
			return invalid("sha256 must be 32 bytes, got %d", len(it.SHA256))
		}
	case KindDir:
		if it.Size != 0 || len(it.SHA256) != 0 {
			return invalid("a directory carries no size or digest")
		}
	default:
		return invalid("unknown kind %d", it.Kind)
	}
	if len(it.Mime) > l.MaxMimeLen {
		return invalid("mime type too long")
	}
	return validPath(it.Path, l)
}

// validPath accepts only a clean, relative, slash-separated path whose every
// component is an ordinary name on this OS. filepath.IsLocal does the
// OS-specific part — on Windows it also rejects reserved names such as NUL
// and COM1, and paths with a volume.
func validPath(p string, l Limits) error {
	switch {
	case p == "":
		return invalid("empty path")
	case len(p) > l.MaxPath:
		return invalid("path longer than %d bytes", l.MaxPath)
	case !utf8.ValidString(p):
		return invalid("path is not UTF-8")
	case strings.ContainsAny(p, "\\\x00"):
		return invalid("path %q contains a backslash or NUL", p)
	case path.Clean(p) != p:
		return invalid("path %q is not clean", p)
	case strings.HasPrefix(p, "/"):
		return invalid("path %q is absolute", p)
	}
	parts := strings.Split(p, "/")
	if len(parts) > l.MaxDepth {
		return invalid("path %q deeper than %d", p, l.MaxDepth)
	}
	for _, part := range parts {
		if part == "." || part == ".." {
			return invalid("path %q has a %q component", p, part)
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f {
				return invalid("path %q contains a control character", p)
			}
		}
		if runtime.GOOS == "windows" && strings.ContainsAny(part, `<>:"|?*`) {
			return invalid("path %q has a character Windows forbids", p)
		}
	}
	if !filepath.IsLocal(filepath.FromSlash(p)) {
		return invalid("path %q is not local", p)
	}
	return nil
}

// foldKey maps paths that one of the common filesystems would treat as the
// same file to one key: macOS normalizes Unicode and, like Windows, ignores
// case by default.
func foldKey(p string) string {
	return strings.ToLower(norm.NFC.String(p))
}
