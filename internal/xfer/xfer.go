// Package xfer is satchel's transfer service: offers, and receiver-pulled
// chunks, multiplexed over one parley session.
//
// The receiver drives every transfer. A sender broadcasts an Offer and then
// only ever answers Pull requests, so it can never outrun a receiver: parley's
// relay drops a member that falls 64 frames behind, and here at most Window
// chunks are ever in flight to anyone. The receiver re-pulls from where it is
// if a chunk goes missing, which is also how a transfer survives a relay
// reconnect.
//
// Everything a peer sends is untrusted. The manifest is validated before the
// sink sees it, every chunk is bounds-checked, and each item's SHA-256 is
// verified before the item is committed.
package xfer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/richardwooding/parley/service"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"

	"github.com/richardwooding/satchel/internal/manifest"
)

const (
	// ID is the parley service ID.
	ID = "xfer"
	// ChunkSize keeps a chunk frame well inside parley's 64 KiB MaxFrame
	// after envelope and AEAD overhead; an oversize send desyncs a session
	// for good, so this is not a knob to turn up.
	ChunkSize = 48 << 10
	// Window is the most chunks a receiver has outstanding.
	Window = 16

	inlineManifestMax = 16 << 10
	inlineDataMax     = 24 << 10
	maxManifestBytes  = 64 << 20
	maxNameRunes      = 120
	stallAfter        = 10 * time.Second
)

// manifestItem is the item index that addresses an offer's manifest itself,
// for offers too large to carry it inline.
const manifestItem = -1

// OfferID names one offer, unique per sender.
type OfferID [16]byte

func (id OfferID) String() string { return hex.EncodeToString(id[:]) }

// Summary is what the receiver's UI shows before anything is accepted. It
// comes from the peer, so it is sanitized on arrival and checked against the
// manifest once that arrives.
type Summary struct {
	Items int           `cbor:"1,keyasint"`
	Bytes int64         `cbor:"2,keyasint"`
	Name  string        `cbor:"3,keyasint"` // first item's top-level name
	Kind  manifest.Kind `cbor:"4,keyasint"` // first item's kind
}

type msgKind uint8

const (
	kindOffer  msgKind = 1
	kindPull   msgKind = 2
	kindChunk  msgKind = 3
	kindCancel msgKind = 4
	kindHello  msgKind = 5
	kindDone   msgKind = 6
)

// msg is every xfer frame; Kind says which fields matter.
type msg struct {
	Kind msgKind `cbor:"1,keyasint"`
	ID   []byte  `cbor:"2,keyasint,omitempty"`

	// offer
	Summary      *Summary `cbor:"3,keyasint,omitempty"`
	Manifest     []byte   `cbor:"4,keyasint,omitempty"` // inline, or nil to pull
	ManifestSize int64    `cbor:"5,keyasint,omitempty"`
	ManifestSHA  []byte   `cbor:"6,keyasint,omitempty"`
	Inline       []byte   `cbor:"7,keyasint,omitempty"` // item 0, single-item offers only

	// pull / chunk
	Item   int32  `cbor:"8,keyasint,omitempty"`
	Offset int64  `cbor:"9,keyasint,omitempty"`
	Count  uint16 `cbor:"10,keyasint,omitempty"`
	Data   []byte `cbor:"11,keyasint,omitempty"`

	// cancel
	Reason string `cbor:"12,keyasint,omitempty"`
}

// Events emitted on the mux stream.
type (
	// Offered: a peer offers something. Answer with Accept or Decline.
	Offered struct {
		From    wire.ParticipantID
		ID      OfferID
		Summary Summary
	}
	// Progress on an accepted offer, in bytes of item data.
	Progress struct {
		From        wire.ParticipantID
		ID          OfferID
		Done, Total int64
	}
	// Received: every item arrived and verified.
	Received struct {
		From     wire.ParticipantID
		ID       OfferID
		Manifest manifest.Manifest
	}
	// Delivered: a receiver reports it has everything (sender side).
	Delivered struct {
		To wire.ParticipantID
		ID OfferID
	}
	// Failed: an accepted offer will not complete.
	Failed struct {
		From wire.ParticipantID
		ID   OfferID
		Err  error
	}
	// Withdrawn: the sender cancelled an offer that was never accepted.
	Withdrawn struct {
		From wire.ParticipantID
		ID   OfferID
	}
)

// ErrWithdrawn is a Failed.Err: the sender cancelled mid-transfer.
var ErrWithdrawn = errors.New("xfer: sender withdrew the offer")

// Service implements parley's service.Service. HandleFrame runs on the mux
// goroutine; the public methods are called from the UI and take mu.
type Service struct {
	service.Base

	mu       sync.Mutex
	outgoing map[OfferID]*outgoing
	incoming map[peerOffer]*incoming

	pulls     chan pullReq
	startOnce sync.Once
	closed    chan struct{}
	closeOnce sync.Once
}

type peerOffer struct {
	from wire.ParticipantID
	id   OfferID
}

// New returns an unattached service. Close it when the session ends.
func New() *Service {
	return &Service{
		outgoing: map[OfferID]*outgoing{},
		incoming: map[peerOffer]*incoming{},
		pulls:    make(chan pullReq, 4*Window),
		closed:   make(chan struct{}),
	}
}

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

// Attach records the context. A joiner then asks every member for the
// offers it missed: offers are broadcast live, and a sender need not be the
// host, so the host's snapshot cannot carry them.
//
// parley's Rebind calls Attach again after a reconnect. Whatever was in
// flight then is gone, so every accepted transfer re-pulls from its last
// received byte at once rather than waiting out the stall timer.
func (s *Service) Attach(ctx service.Context) {
	s.SetContext(ctx)
	if !ctx.Host {
		_ = s.broadcast(msg{Kind: kindHello})
	}
	s.mu.Lock()
	var repull []effects
	for _, in := range s.incoming {
		if in.accepted {
			in.requested = in.next
			repull = append(repull, s.requestMore(in))
		}
	}
	s.mu.Unlock()
	for _, e := range repull {
		s.apply(e)
	}
}

// Snapshot and Restore are unused: offers reach late joiners through hello,
// and chunk data must never ride a snapshot, which parley caps at one frame.
func (s *Service) Snapshot() ([]byte, error) { return nil, nil }
func (s *Service) Restore([]byte) error      { return nil }

// Close stops the sender worker and every pending stall timer.
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, in := range s.incoming {
			in.stopTimer()
		}
	})
}

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	m, err := wire.Body[msg](body)
	if err != nil {
		return fmt.Errorf("xfer: %w", err)
	}
	switch m.Kind {
	case kindHello:
		s.handleHello(from)
	case kindOffer:
		return s.handleOffer(from, m)
	case kindPull:
		return s.handlePull(from, m)
	case kindChunk:
		return s.handleChunk(from, m)
	case kindCancel:
		s.handleCancel(from, m)
	case kindDone:
		s.handleDone(from, m)
	default:
		return fmt.Errorf("xfer: unknown frame kind %d from %d", m.Kind, from)
	}
	return nil
}

// MemberKeyed is part of service.MemberObserver; offers reach newcomers via
// their hello instead, which also covers members who are not the host.
func (s *Service) MemberKeyed(wire.ParticipantID, session.Role) {}

// MemberLeft fails whatever that member was sending us. Its pulls die with
// it: the worker drops requests whose sender is gone when SendTo fails.
func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	var failed []*incoming
	for k, in := range s.incoming {
		if k.from == id {
			failed = append(failed, in)
			delete(s.incoming, k)
		}
	}
	s.mu.Unlock()
	for _, in := range failed {
		s.fail(in, errors.New("xfer: sender left the session"))
	}
}

func (s *Service) broadcast(m msg) error {
	b, err := wire.Marshal(m)
	if err != nil {
		return err
	}
	return s.Ctx().Send.Broadcast(ID, b)
}

func (s *Service) sendTo(to wire.ParticipantID, m msg) error {
	b, err := wire.Marshal(m)
	if err != nil {
		return err
	}
	return s.Ctx().Send.SendTo(to, ID, b)
}

func (s *Service) emit(ev any) {
	if e := s.Ctx().Emit; e != nil {
		e(ev)
	}
}

func newOfferID() (OfferID, error) {
	var id OfferID
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("xfer: rand: %w", err)
	}
	return id, nil
}

func offerID(b []byte) (OfferID, bool) {
	var id OfferID
	if len(b) != len(id) {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

// cleanName makes a peer-supplied name safe to show: printable runes only,
// bounded length. It is display text, never a path.
func cleanName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == maxNameRunes {
			b.WriteString("…")
			break
		}
		if !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
