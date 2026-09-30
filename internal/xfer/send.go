package xfer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/richardwooding/parley/wire"

	"github.com/richardwooding/satchel/internal/manifest"
)

// Source serves the bytes of an outgoing offer's items. ReadAt may be called
// concurrently and for any item, any number of times — every receiver pulls
// independently, and a receiver re-pulls after a stall.
type Source interface {
	ReadAt(item int, p []byte, off int64) (int, error)
}

// Bytes is a Source over in-memory items, indexed like the manifest.
type Bytes [][]byte

func (b Bytes) ReadAt(item int, p []byte, off int64) (int, error) {
	if item < 0 || item >= len(b) {
		return 0, fmt.Errorf("xfer: no item %d", item)
	}
	return bytes.NewReader(b[item]).ReadAt(p, off)
}

// ItemFor builds a manifest item for in-memory data, such as clipboard text.
func ItemFor(name string, kind manifest.Kind, mime string, data []byte) manifest.Item {
	sum := sha256.Sum256(data)
	return manifest.Item{Path: name, Kind: kind, Size: int64(len(data)), SHA256: sum[:], Mime: mime}
}

type outgoing struct {
	id       OfferID
	offer    msg // the offer frame, replayed to latecomers
	manifest []byte
	items    []manifest.Item
	src      Source
}

type pullReq struct {
	to    wire.ParticipantID
	id    OfferID
	item  int
	off   int64
	count int
}

// Offer broadcasts an offer of m, served from src, to everyone in the session
// now and to anyone who joins while it stands. It validates m against the
// default limits first, so a sender cannot build an offer every receiver
// would refuse.
func (s *Service) Offer(m manifest.Manifest, src Source) (OfferID, error) {
	if err := m.Validate(manifest.Default); err != nil {
		return OfferID{}, err
	}
	raw, err := m.Encode()
	if err != nil {
		return OfferID{}, err
	}
	id, err := newOfferID()
	if err != nil {
		return OfferID{}, err
	}
	o := &outgoing{id: id, manifest: raw, items: m.Items, src: src}
	o.offer = offerFrame(id, m, raw)
	if err := s.inlineSmall(o); err != nil {
		return OfferID{}, err
	}

	s.startOnce.Do(func() { go s.serve() })
	s.mu.Lock()
	s.outgoing[id] = o
	s.mu.Unlock()
	if err := s.broadcast(o.offer); err != nil {
		return id, err
	}
	return id, nil
}

func offerFrame(id OfferID, m manifest.Manifest, raw []byte) msg {
	first := m.Items[0]
	top, _, _ := strings.Cut(first.Path, "/")
	sum := sha256.Sum256(raw)
	f := msg{
		Kind: kindOffer,
		ID:   id[:],
		Summary: &Summary{
			Items: len(m.Items), Bytes: m.Total(), Name: path.Base(top), Kind: first.Kind,
		},
		ManifestSize: int64(len(raw)),
		ManifestSHA:  sum[:],
	}
	if len(raw) <= inlineManifestMax {
		f.Manifest = raw
	}
	return f
}

// inlineSmall carries a lone small item — clipboard text, a screenshot —
// inside the offer, so accepting it needs no round trip at all.
func (s *Service) inlineSmall(o *outgoing) error {
	if len(o.items) != 1 || o.offer.Manifest == nil || o.items[0].Size > inlineDataMax {
		return nil
	}
	buf := make([]byte, o.items[0].Size)
	if _, err := o.src.ReadAt(0, buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("xfer: read item 0: %w", err)
	}
	o.offer.Inline = buf
	return nil
}

// Withdraw cancels an offer: no new pulls are served, and anyone mid-transfer
// is told to stop.
func (s *Service) Withdraw(id OfferID) error {
	s.mu.Lock()
	_, ok := s.outgoing[id]
	delete(s.outgoing, id)
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return s.broadcast(msg{Kind: kindCancel, ID: id[:], Reason: "withdrawn"})
}

// handleHello replays every standing offer to a newcomer.
func (s *Service) handleHello(from wire.ParticipantID) {
	s.mu.Lock()
	offers := make([]msg, 0, len(s.outgoing))
	for _, o := range s.outgoing {
		offers = append(offers, o.offer)
	}
	s.mu.Unlock()
	for _, f := range offers {
		_ = s.sendTo(from, f)
	}
}

func (s *Service) handlePull(from wire.ParticipantID, m msg) error {
	id, ok := offerID(m.ID)
	if !ok || m.Count == 0 || m.Count > Window || m.Offset < 0 {
		return fmt.Errorf("xfer: malformed pull from %d", from)
	}
	s.mu.Lock()
	_, known := s.outgoing[id]
	s.mu.Unlock()
	if !known {
		// Withdrawn, or never ours: say so rather than leave them waiting.
		return s.sendTo(from, msg{Kind: kindCancel, ID: id[:], Reason: "no such offer"})
	}
	req := pullReq{to: from, id: id, item: int(m.Item), off: m.Offset, count: int(m.Count)}
	select {
	case s.pulls <- req:
	default:
		// The worker is saturated. Dropping is safe: the receiver re-pulls
		// when it stalls, and blocking here would stall the whole mux.
	}
	return nil
}

// serve answers pulls one at a time, off the mux goroutine: a pull is up to
// Window synchronous frame writes.
func (s *Service) serve() {
	for {
		select {
		case <-s.closed:
			return
		case r := <-s.pulls:
			s.answer(r)
		}
	}
}

func (s *Service) answer(r pullReq) {
	s.mu.Lock()
	o, ok := s.outgoing[r.id]
	s.mu.Unlock()
	if !ok {
		return
	}
	size, read, err := o.reader(r.item)
	if err != nil {
		_ = s.sendTo(r.to, msg{Kind: kindCancel, ID: r.id[:], Reason: err.Error()})
		return
	}
	off := r.off
	for range r.count {
		if off >= size {
			return
		}
		buf := make([]byte, min(ChunkSize, size-off))
		n, err := read(buf, off)
		if err != nil && (!errors.Is(err, io.EOF) || n != len(buf)) {
			_ = s.sendTo(r.to, msg{Kind: kindCancel, ID: r.id[:], Reason: "read failed"})
			return
		}
		chunk := msg{Kind: kindChunk, ID: r.id[:], Item: int32(r.item), Offset: off, Data: buf[:n]}
		if s.sendTo(r.to, chunk) != nil {
			return // receiver gone or connection down; it will re-pull
		}
		off += int64(n)
	}
}

// reader resolves an item index — or manifestItem — to its size and bytes.
func (o *outgoing) reader(item int) (int64, func([]byte, int64) (int, error), error) {
	if item == manifestItem {
		return int64(len(o.manifest)), bytes.NewReader(o.manifest).ReadAt, nil
	}
	if item < 0 || item >= len(o.items) || o.items[item].Kind == manifest.KindDir {
		return 0, nil, fmt.Errorf("no item %d", item)
	}
	return o.items[item].Size, func(p []byte, off int64) (int, error) { return o.src.ReadAt(item, p, off) }, nil
}

func (s *Service) handleDone(from wire.ParticipantID, m msg) {
	if id, ok := offerID(m.ID); ok {
		s.emit(Delivered{To: from, ID: id})
	}
}
