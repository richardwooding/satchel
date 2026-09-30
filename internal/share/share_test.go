package share

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/richardwooding/parley/relay"

	"github.com/richardwooding/satchel/internal/manifest"
	"github.com/richardwooding/satchel/internal/sink"
	"github.com/richardwooding/satchel/internal/xfer"
)

func relayURL(t *testing.T) string {
	t.Helper()
	s := relay.New(relay.Options{Grace: 5 * time.Second, ConnRate: rate.Inf})
	t.Cleanup(s.Close)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

// events runs a session's loop into a channel.
func events(t *testing.T, s *Session) <-chan any {
	ch := make(chan any, 1024)
	go s.Run(func(ev any) { ch <- ev })
	t.Cleanup(s.Close)
	return ch
}

func next[E any](t *testing.T, ch <-chan any) E {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if e, ok := ev.(E); ok {
				return e
			}
		case <-deadline:
			var zero E
			t.Fatalf("no %T", zero)
		}
	}
}

func TestMistypedPhraseIsNoSuchSession(t *testing.T) {
	url := relayURL(t)
	h, err := Host(ctx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	events(t, h)
	typo := h.Phrase() + "x"
	if _, err := Join(ctx(t), url, typo); !errors.Is(err, ErrNoSuchSession) {
		t.Fatalf("join %q: got %v, want ErrNoSuchSession", typo, err)
	}
}

func TestPhraseTaken(t *testing.T) {
	url := relayURL(t)
	h, err := HostPhrase(ctx(t), url, "otter-7-canyon")
	if err != nil {
		t.Fatal(err)
	}
	events(t, h)
	if _, err := HostPhrase(ctx(t), url, "otter-7-canyon"); !errors.Is(err, ErrPhraseTaken) {
		t.Fatalf("got %v, want ErrPhraseTaken", err)
	}
}

// A dropped connection resumes by itself, and the session still carries
// transfers afterwards.
func TestSessionSurvivesDrop(t *testing.T) {
	url := relayURL(t)
	h, err := Host(ctx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	hev := events(t, h)
	j, err := Join(ctx(t), url, h.Phrase())
	if err != nil {
		t.Fatal(err)
	}
	jev := events(t, j)
	if p := next[Peers](t, hev); p.Count != 1 {
		t.Fatalf("host sees %d peers", p.Count)
	}

	_ = j.client.CloseNow()
	next[Reconnecting](t, jev)
	next[Resumed](t, jev)

	text := []byte("after the drop")
	m := manifest.Manifest{Items: []manifest.Item{xfer.ItemFor("t.txt", manifest.KindText, "text/plain", text)}}
	if _, err := h.X.Offer(m, xfer.Bytes{text}); err != nil {
		t.Fatal(err)
	}
	off := next[xfer.Offered](t, jev)
	mem := sink.NewMemory(1 << 10)
	if err := j.X.Accept(off.From, off.ID, mem, manifest.Default); err != nil {
		t.Fatal(err)
	}
	next[xfer.Received](t, jev)
	if got, _ := mem.Item(0); string(got) != string(text) {
		t.Fatalf("got %q", got)
	}
}

func TestCloseEndsRun(t *testing.T) {
	url := relayURL(t)
	h, err := Host(ctx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	ev := events(t, h)
	h.Close()
	if c := next[Closed](t, ev); c.Reason != "left" {
		t.Fatalf("closed with %q", c.Reason)
	}
}

// The per-IP connection limit surfaces as a failed WebSocket handshake; it
// must still reach the person as "wait a minute", not as raw HTTP.
func TestConnectionLimitIsBusy(t *testing.T) {
	s := relay.New(relay.Options{ConnRate: rate.Every(time.Hour), ConnBurst: 1})
	t.Cleanup(s.Close)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	h, err := Host(ctx(t), url)
	if err != nil {
		t.Fatal(err)
	}
	events(t, h)
	if _, err := Join(ctx(t), url, h.Phrase()); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
}
