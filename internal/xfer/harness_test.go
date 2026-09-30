package xfer

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/richardwooding/parley/relay"
	"github.com/richardwooding/parley/service"
	"github.com/richardwooding/parley/session"

	"github.com/richardwooding/satchel/internal/proto"
)

// relayURL starts a real parley relay. Every test dials from 127.0.0.1, so
// the per-IP connect limit is lifted.
func relayURL(t *testing.T) string {
	t.Helper()
	s := relay.New(relay.Options{Grace: 5 * time.Second, ConnRate: rate.Inf})
	t.Cleanup(s.Close)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

type peer struct {
	c      *session.Client
	mux    *service.Mux
	x      *Service
	events chan any
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func attach(t *testing.T, c *session.Client) *peer {
	t.Helper()
	p := &peer{c: c, x: New(), events: make(chan any, 1<<16)}
	p.mux = service.NewMux(c, service.WithServices(p.x))
	p.mux.SetReconnectable()
	// Drain the mux into a deep buffer so a slow test never stalls it.
	go func() {
		for ev := range p.mux.Events() {
			p.events <- ev
		}
		close(p.events)
	}()
	t.Cleanup(func() {
		p.x.Close()
		p.mux.Close()
		_ = c.Close()
	})
	return p
}

func host(t *testing.T, url string) (*peer, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	return attach(t, c), phrase
}

func join(t *testing.T, url, phrase string) *peer {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	return attach(t, c)
}

// await returns the next event of type E, skipping others. A Failed event
// fails the test unless Failed is what the caller is waiting for.
func await[E any](t *testing.T, p *peer, within time.Duration) E {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case ev, ok := <-p.events:
			if !ok {
				t.Fatal("event stream closed")
			}
			if e, ok := ev.(E); ok {
				return e
			}
			if f, ok := ev.(Failed); ok {
				t.Fatalf("transfer failed: %v", f.Err)
			}
		case <-deadline:
			var zero E
			t.Fatalf("no %T within %v", zero, within)
		}
	}
}
