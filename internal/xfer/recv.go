package xfer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"time"

	"github.com/richardwooding/parley/wire"

	"github.com/richardwooding/satchel/internal/manifest"
	"github.com/richardwooding/satchel/internal/sink"
)

// progressEvery bounds Progress events to one per this many chunks.
const progressEvery = Window

// incoming is one peer's offer to us, from Offered through Received/Failed.
type incoming struct {
	from  wire.ParticipantID
	id    OfferID
	offer msg

	accepted bool
	sink     sink.Sink
	limits   manifest.Limits
	man      manifest.Manifest

	item      int // current item; manifestItem while fetching the manifest
	manBuf    []byte
	w         sink.ItemWriter
	h         hash.Hash
	next      int64 // next byte expected within the current item
	requested int64 // bytes of the current item already asked for
	done      int64 // item bytes received, for Progress
	chunks    int

	timer *time.Timer
}

// effects are what a handler decided while holding mu, carried out after it
// is released: never block the mux goroutine on a network write under mu, and
// never call into the UI (Emit) with it held.
type effects struct {
	to     wire.ParticipantID
	send   []msg
	events []any
}

func (s *Service) apply(e effects) {
	for _, m := range e.send {
		_ = s.sendTo(e.to, m)
	}
	for _, ev := range e.events {
		s.emit(ev)
	}
}

func (s *Service) handleOffer(from wire.ParticipantID, m msg) error {
	id, ok := offerID(m.ID)
	if !ok || m.Summary == nil || len(m.ManifestSHA) != sha256.Size ||
		m.ManifestSize <= 0 || m.ManifestSize > maxManifestBytes ||
		(m.Manifest != nil && int64(len(m.Manifest)) != m.ManifestSize) {
		return fmt.Errorf("xfer: malformed offer from %d", from)
	}
	sum := *m.Summary
	sum.Name = cleanName(sum.Name)
	key := peerOffer{from, id}
	s.mu.Lock()
	_, dup := s.incoming[key]
	if !dup {
		s.incoming[key] = &incoming{from: from, id: id, offer: m, item: manifestItem}
	}
	s.mu.Unlock()
	if !dup { // a hello replay of an offer we already hold
		s.emit(Offered{From: from, ID: id, Summary: sum})
	}
	return nil
}

// Decline forgets an offer. The sender is not told: it serves only what is
// pulled, so a declined offer costs it nothing.
func (s *Service) Decline(from wire.ParticipantID, id OfferID) {
	s.mu.Lock()
	delete(s.incoming, peerOffer{from, id})
	s.mu.Unlock()
}

// Accept starts pulling an offer into snk, refusing anything outside l.
func (s *Service) Accept(from wire.ParticipantID, id OfferID, snk sink.Sink, l manifest.Limits) error {
	s.mu.Lock()
	in, ok := s.incoming[peerOffer{from, id}]
	if !ok || in.accepted {
		s.mu.Unlock()
		return fmt.Errorf("xfer: no pending offer %s from %d", id, from)
	}
	in.accepted, in.sink, in.limits = true, snk, l
	in.timer = time.AfterFunc(stallAfter, func() { s.stalled(in) })
	var e effects
	var err error
	if in.offer.Manifest != nil {
		e, err = s.begin(in, in.offer.Manifest)
	} else {
		in.h = sha256.New()
		e = s.requestMore(in)
	}
	s.mu.Unlock()
	s.apply(e)
	return err
}

func (s *Service) handleChunk(from wire.ParticipantID, m msg) error {
	id, ok := offerID(m.ID)
	if !ok {
		return fmt.Errorf("xfer: malformed chunk from %d", from)
	}
	s.mu.Lock()
	in := s.incoming[peerOffer{from, id}]
	if in == nil || !in.accepted || int(m.Item) != in.item || m.Offset != in.next {
		// Unknown, not accepted, or a duplicate from a pull we re-issued
		// after a stall. Only the chunk at exactly the next offset counts.
		s.mu.Unlock()
		return nil
	}
	e, err := s.consume(in, m.Data)
	s.mu.Unlock()
	s.apply(e)
	return err
}

// consume takes the next chunk of the current item. Called with mu held.
func (s *Service) consume(in *incoming, data []byte) (effects, error) {
	size := in.size()
	if len(data) == 0 || len(data) > ChunkSize || in.next+int64(len(data)) > size {
		return s.failLocked(in, fmt.Errorf("xfer: chunk overruns item %d", in.item)), nil
	}
	if err := in.write(data); err != nil {
		return s.failLocked(in, err), nil
	}
	in.next += int64(len(data))
	in.timer.Reset(stallAfter)
	var e effects
	if in.item != manifestItem {
		in.done += int64(len(data))
		if in.chunks++; in.chunks%progressEvery == 0 {
			e.events = append(e.events, Progress{From: in.from, ID: in.id, Done: in.done, Total: in.man.Total()})
		}
	}
	if in.next < size {
		more := s.requestMore(in)
		e.to, e.send = more.to, more.send
		return e, nil
	}
	rest, err := s.finishItem(in)
	e.to = rest.to
	e.send = append(e.send, rest.send...)
	e.events = append(e.events, rest.events...)
	return e, err
}

func (in *incoming) size() int64 {
	if in.item == manifestItem {
		return in.offer.ManifestSize
	}
	return in.man.Items[in.item].Size
}

func (in *incoming) write(data []byte) error {
	in.h.Write(data)
	if in.item == manifestItem {
		in.manBuf = append(in.manBuf, data...)
		return nil
	}
	_, err := in.w.Write(data)
	return err
}

// finishItem verifies the item just completed and moves on. Called with mu.
func (s *Service) finishItem(in *incoming) (effects, error) {
	if in.item == manifestItem {
		if !bytes.Equal(in.h.Sum(nil), in.offer.ManifestSHA) {
			return s.failLocked(in, errors.New("xfer: manifest digest mismatch")), nil
		}
		return s.begin(in, in.manBuf)
	}
	w := in.w
	in.w = nil
	if !bytes.Equal(in.h.Sum(nil), in.man.Items[in.item].SHA256) {
		_ = w.Abort()
		return s.failLocked(in, fmt.Errorf("xfer: %q failed verification", in.man.Items[in.item].Path)), nil
	}
	if err := w.Commit(); err != nil {
		return s.failLocked(in, err), nil
	}
	return s.advance(in), nil
}

// begin validates the manifest, prepares the sink, and starts on the items.
// Called with mu held.
func (s *Service) begin(in *incoming, raw []byte) (effects, error) {
	if !bytes.Equal(sha256Sum(raw), in.offer.ManifestSHA) {
		return s.failLocked(in, errors.New("xfer: manifest digest mismatch")), nil
	}
	m, err := manifest.Decode(raw, in.limits)
	if err != nil {
		return s.failLocked(in, err), nil
	}
	if sum := in.offer.Summary; sum.Items != len(m.Items) || sum.Bytes != m.Total() {
		return s.failLocked(in, errors.New("xfer: offer summary does not match its manifest")), nil
	}
	in.man, in.manBuf = m, nil
	if err := in.sink.Prepare(m); err != nil {
		return s.failLocked(in, err), nil
	}
	if in.offer.Inline != nil {
		return s.takeInline(in), nil
	}
	return s.advance(in), nil
}

// takeInline completes a single-item offer whose data rode in the offer.
func (s *Service) takeInline(in *incoming) effects {
	it := in.man.Items[0]
	data := in.offer.Inline
	if len(in.man.Items) != 1 || it.Kind == manifest.KindDir || int64(len(data)) != it.Size ||
		!bytes.Equal(sha256Sum(data), it.SHA256) {
		return s.failLocked(in, errors.New("xfer: inline data does not match its manifest"))
	}
	w, err := in.sink.Create(0)
	if err == nil {
		if _, err = w.Write(data); err == nil {
			err = w.Commit()
		} else {
			_ = w.Abort()
		}
	}
	if err != nil {
		return s.failLocked(in, err)
	}
	in.item, in.done = 0, it.Size
	return s.advance(in)
}

// advance moves to the next item that needs bytes, materializing
// directories and empty files on the way; after the last, it finishes.
// Called with mu held.
func (s *Service) advance(in *incoming) effects {
	for in.item++; in.item < len(in.man.Items); in.item++ {
		it := in.man.Items[in.item]
		if it.Kind == manifest.KindDir {
			if err := in.sink.Mkdir(in.item); err != nil {
				return s.failLocked(in, err)
			}
			continue
		}
		w, err := in.sink.Create(in.item)
		if err != nil {
			return s.failLocked(in, err)
		}
		in.w, in.h, in.next, in.requested = w, sha256.New(), 0, 0
		if it.Size > 0 {
			return s.requestMore(in)
		}
		// An empty file has nothing to pull: verify and commit it now, and
		// finishItem carries on to the item after it.
		e, _ := s.finishItem(in)
		return e
	}
	return s.complete(in)
}

func (s *Service) complete(in *incoming) effects {
	in.stopTimer()
	delete(s.incoming, peerOffer{in.from, in.id})
	e := effects{to: in.from, send: []msg{{Kind: kindDone, ID: in.id[:]}}}
	if err := in.sink.Finish(); err != nil {
		e.send = nil
		e.events = append(e.events, Failed{From: in.from, ID: in.id, Err: err})
		return e
	}
	e.events = append(e.events,
		Progress{From: in.from, ID: in.id, Done: in.man.Total(), Total: in.man.Total()},
		Received{From: in.from, ID: in.id, Manifest: in.man})
	return e
}

// requestMore keeps up to Window chunks of the current item in flight,
// topping up once half have arrived. Called with mu held.
func (s *Service) requestMore(in *incoming) effects {
	size := in.size()
	outstanding := int((in.requested - in.next + ChunkSize - 1) / ChunkSize)
	if in.requested >= size || outstanding > Window/2 {
		return effects{}
	}
	left := int((size - in.requested + ChunkSize - 1) / ChunkSize)
	count := min(Window-outstanding, left)
	pull := msg{Kind: kindPull, ID: in.id[:], Item: int32(in.item), Offset: in.requested, Count: uint16(count)}
	in.requested = min(size, in.requested+int64(count)*ChunkSize)
	return effects{to: in.from, send: []msg{pull}}
}

// stalled re-asks from the last byte actually received: the chunks in
// flight were lost to a reconnect, or dropped by a saturated sender.
func (s *Service) stalled(in *incoming) {
	s.mu.Lock()
	if s.incoming[peerOffer{in.from, in.id}] != in {
		s.mu.Unlock()
		return
	}
	in.requested = in.next
	e := s.requestMore(in)
	in.timer.Reset(stallAfter)
	s.mu.Unlock()
	s.apply(e)
}

func (s *Service) handleCancel(from wire.ParticipantID, m msg) {
	id, ok := offerID(m.ID)
	if !ok {
		return
	}
	s.mu.Lock()
	in := s.incoming[peerOffer{from, id}]
	var e effects
	switch {
	case in == nil:
	case in.accepted:
		e = s.failLocked(in, fmt.Errorf("%w: %s", ErrWithdrawn, cleanName(m.Reason)))
	default:
		delete(s.incoming, peerOffer{from, id})
		e.events = []any{Withdrawn{From: from, ID: id}}
	}
	s.mu.Unlock()
	s.apply(e)
}

// failLocked tears down an accepted offer. Called with mu held.
func (s *Service) failLocked(in *incoming, err error) effects {
	delete(s.incoming, peerOffer{in.from, in.id})
	s.teardown(in)
	return effects{events: []any{Failed{From: in.from, ID: in.id, Err: err}}}
}

// fail is failLocked for an offer already removed from the map.
func (s *Service) fail(in *incoming, err error) {
	s.mu.Lock()
	s.teardown(in)
	s.mu.Unlock()
	if in.accepted {
		s.emit(Failed{From: in.from, ID: in.id, Err: err})
	} else {
		s.emit(Withdrawn{From: in.from, ID: in.id})
	}
}

func (s *Service) teardown(in *incoming) {
	in.stopTimer()
	if in.w != nil {
		_ = in.w.Abort()
		in.w = nil
	}
	if in.sink != nil {
		_ = in.sink.Abandon()
		in.sink = nil
	}
}

func (in *incoming) stopTimer() {
	if in.timer != nil {
		in.timer.Stop()
	}
}

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}
