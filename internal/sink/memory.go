package sink

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/richardwooding/satchel/internal/manifest"
)

// Memory keeps items in memory, for clipboard contents and for the browser,
// which has no filesystem to write to. Max bounds the bytes it will hold.
type Memory struct {
	Max int64

	mu       sync.Mutex
	manifest manifest.Manifest
	items    map[int][]byte
	held     int64
	done     bool
}

// NewMemory returns a sink holding at most max bytes.
func NewMemory(max int64) *Memory { return &Memory{Max: max} }

func (m *Memory) Prepare(man manifest.Manifest) error {
	if t := man.Total(); t > m.Max {
		return fmt.Errorf("%w: offer is %d bytes, limit %d", ErrTooLarge, t, m.Max)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.manifest, m.items = man, map[int][]byte{}
	return nil
}

func (m *Memory) Mkdir(int) error { return nil }

func (m *Memory) Create(i int) (ItemWriter, error) { return &memWriter{m: m, i: i}, nil }

func (m *Memory) Finish() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done = true
	return nil
}

func (m *Memory) Abandon() error { return nil }

// Item returns a committed item's bytes.
func (m *Memory) Item(i int) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.items[i]
	return b, ok
}

// Manifest returns what Prepare was given.
func (m *Memory) Manifest() manifest.Manifest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.manifest
}

type memWriter struct {
	m   *Memory
	i   int
	buf bytes.Buffer
}

func (w *memWriter) Write(p []byte) (int, error) {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	if w.m.held+int64(len(p)) > w.m.Max {
		return 0, ErrTooLarge
	}
	w.m.held += int64(len(p))
	return w.buf.Write(p)
}

func (w *memWriter) Commit() error {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.m.items[w.i] = w.buf.Bytes()
	return nil
}

func (w *memWriter) Abort() error {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.m.held -= int64(w.buf.Len())
	return nil
}
