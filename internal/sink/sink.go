// Package sink is where received items land. Every sink is written to
// sequentially, one item at a time, and an item becomes visible only when it
// is committed — which the transfer service does only after the item's digest
// has checked out.
package sink

import (
	"errors"
	"io"

	"github.com/richardwooding/satchel/internal/manifest"
)

// Sink receives one offer. Calls arrive in manifest order from one goroutine.
type Sink interface {
	// Prepare is called once, with a manifest that already validated, before
	// any item.
	Prepare(m manifest.Manifest) error
	// Mkdir materializes directory item i.
	Mkdir(i int) error
	// Create opens item i for writing from its first byte.
	Create(i int) (ItemWriter, error)
	// Finish is called after every item committed. Abandon is called instead
	// when the transfer fails or is cancelled; it must discard uncommitted
	// data and may keep committed items.
	Finish() error
	Abandon() error
}

// ItemWriter accumulates one item. Exactly one of Commit or Abort is called.
type ItemWriter interface {
	io.Writer
	Commit() error
	Abort() error
}

// ErrTooLarge is returned when a sink's own capacity would be exceeded.
var ErrTooLarge = errors.New("sink: item exceeds capacity")
