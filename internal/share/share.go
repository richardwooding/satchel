// Package share is one satchel session as every surface uses it — the
// browser core and the tray app alike: host or join by phrase, carry the xfer
// service, and keep the connection alive across network drops.
//
// It has no UI and no syscall/js, so it is tested natively against a real
// relay and the browser runs exactly the same code.
package share

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/richardwooding/parley/service"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"

	"github.com/richardwooding/satchel/internal/proto"
	"github.com/richardwooding/satchel/internal/xfer"
)

// Events, alongside the xfer events that are passed through unchanged.
type (
	// Peers is how many other members are keyed into the session.
	Peers struct{ Count int }
	// Reconnecting: the relay connection dropped; retrying.
	Reconnecting struct{}
	// Resumed: back on the same session, same keys; transfers carry on.
	Resumed struct{}
	// Closed: the session is over. Reason is parley's, for display.
	Closed struct{ Reason string }
)

// Session is one live parley session with the xfer service on it.
type Session struct {
	X      *xfer.Service
	phrase string

	client *session.Client
	mux    *service.Mux

	closeOnce sync.Once
	done      chan struct{}
}

// Host opens a new session and returns it with its fresh phrase. extra
// services run alongside xfer — pairing adds its own.
func Host(ctx context.Context, relayURL string, extra ...service.Service) (*Session, error) {
	c, phrase, err := session.Host(ctx, relayURL, proto.Options()...)
	if err != nil {
		return nil, friendly(err)
	}
	return start(c, phrase, extra), nil
}

// HostPhrase opens a session under a phrase the caller chose — paired devices
// meet on a phrase both derive from their shared secret.
func HostPhrase(ctx context.Context, relayURL, phrase string, extra ...service.Service) (*Session, error) {
	c, err := session.HostWithPhrase(ctx, relayURL, phrase, proto.Options()...)
	if err != nil {
		return nil, friendly(err)
	}
	return start(c, phrase, extra), nil
}

// Join enters an existing session by phrase.
func Join(ctx context.Context, relayURL, phrase string, extra ...service.Service) (*Session, error) {
	c, err := session.Join(ctx, relayURL, phrase, proto.Options()...)
	if err != nil {
		return nil, friendly(err)
	}
	return start(c, phrase, extra), nil
}

func start(c *session.Client, phrase string, extra []service.Service) *Session {
	x := xfer.New()
	mux := service.NewMux(c, service.WithServices(append([]service.Service{x}, extra...)...))
	mux.SetReconnectable()
	return &Session{X: x, phrase: phrase, client: c, mux: mux, done: make(chan struct{})}
}

// Phrase is the session's code phrase.
func (s *Session) Phrase() string { return s.phrase }

// Self is this end's participant ID.
func (s *Session) Self() wire.ParticipantID { return s.client.Self() }

// Close leaves the session cleanly. Run returns soon after.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.X.Close()
		_ = s.client.Close()
		s.mux.Close()
	})
}

// Run delivers events to onEvent until the session ends, reconnecting after
// network drops. It returns after emitting Closed.
func (s *Session) Run(onEvent func(any)) {
	for ev := range s.mux.Events() {
		switch e := ev.(type) {
		case service.Roster:
			onEvent(Peers{Count: max(0, len(e.Members)-1)})
		case service.SessionEvent:
			closed, ok := e.Event.(session.Closed)
			if !ok {
				continue
			}
			if s.closing() {
				onEvent(Closed{Reason: "left"})
				return
			}
			if closed.Reason == "connection lost" && s.reconnect(onEvent) {
				continue
			}
			onEvent(Closed{Reason: closed.Reason})
			s.Close()
			return
		case service.Desync:
			// Expected from xfer: one sender's chunks to several receivers
			// share a sequence counter. Chunks are matched by offset, so it
			// carries no information here.
		default:
			onEvent(ev)
		}
	}
	onEvent(Closed{Reason: "left"})
}

func (s *Session) closing() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// Backoff between reconnect attempts. The relay holds a dropped slot for 30s
// and allows 5 connection attempts a minute per IP, so this spends five
// attempts across roughly the grace window.
var backoff = []time.Duration{0, time.Second, 3 * time.Second, 8 * time.Second, 15 * time.Second}

func (s *Session) reconnect(onEvent func(any)) bool {
	onEvent(Reconnecting{})
	for _, wait := range backoff {
		select {
		case <-s.done:
			return false
		case <-time.After(wait):
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err := s.client.Reconnect(ctx)
		cancel()
		if err == nil {
			s.mux.Rebind(s.client)
			onEvent(Resumed{})
			return true
		}
		var re *session.RelayError
		if errors.As(err, &re) && re.Code == wire.ErrCodeResumeRejected {
			return false // the slot is gone; retrying cannot help
		}
	}
	return false
}

// Errors a person can act on. There is no "wrong phrase" error: the session
// ID is derived from the phrase, so a mistyped phrase names a session that
// does not exist.
var (
	ErrNoSuchSession = errors.New("no session with that phrase — check for typos")
	ErrFull          = errors.New("that session is full")
	ErrBusy          = errors.New("too many attempts from this network — wait a minute")
	ErrRelayFull     = errors.New("the relay is at capacity — try again shortly")
	ErrPhraseTaken   = errors.New("a session with that phrase is already open")
)

func friendly(err error) error {
	switch {
	case errors.Is(err, session.ErrSessionNotFound):
		return ErrNoSuchSession
	case errors.Is(err, session.ErrSessionFull):
		return ErrFull
	case errors.Is(err, session.ErrRateLimited):
		// Despite the name, the relay sends this code when it holds its
		// maximum number of sessions.
		return ErrRelayFull
	case errors.Is(err, session.ErrSessionExists):
		return ErrPhraseTaken
	case strings.Contains(err.Error(), "429"):
		// The per-IP connection limit refuses the WebSocket upgrade itself,
		// so it arrives as a failed handshake, not as a relay error code.
		return ErrBusy
	}
	return err
}
