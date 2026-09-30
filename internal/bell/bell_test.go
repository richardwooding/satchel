package bell

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func setup(t *testing.T, opt Options) (*Server, *Client) {
	t.Helper()
	s := New(opt)
	mux := http.NewServeMux()
	mux.Handle("/bell", s)
	mux.Handle("/bell/", s)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, &Client{Base: srv.URL}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

var a, b = ID{1}, ID{2}

func TestRingWakesAWaitingPoll(t *testing.T) {
	_, c := setup(t, Options{})
	got := make(chan []Ring, 1)
	go func() {
		r, err := c.Poll(ctx(t), []ID{a, b})
		if err != nil {
			t.Error(err)
		}
		got <- r
	}()
	time.Sleep(100 * time.Millisecond) // let the poll start waiting
	if err := c.Ring(ctx(t), b, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if len(r) != 1 || r[0].ID != b.String() || !bytes.Equal(r[0].Data, []byte("hello")) {
			t.Fatalf("got %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("poll never woke")
	}
}

// A receiver between polls must still hear a ring.
func TestRingIsHeldUntilPolled(t *testing.T) {
	s, c := setup(t, Options{})
	if err := c.Ring(ctx(t), a, []byte("x")); err != nil {
		t.Fatal(err)
	}
	r, err := c.Poll(ctx(t), []ID{a})
	if err != nil || len(r) != 1 {
		t.Fatalf("got %v, %v", r, err)
	}
	if s.Held() != 0 {
		t.Fatalf("server still holds %d inboxes after delivery", s.Held())
	}
}

func TestUnheardRingExpires(t *testing.T) {
	_, c := setup(t, Options{Pending: 50 * time.Millisecond, Wait: 50 * time.Millisecond})
	_ = c.Ring(ctx(t), a, []byte("x"))
	time.Sleep(100 * time.Millisecond)
	if r, err := c.Poll(ctx(t), []ID{a}); err != nil || len(r) != 0 {
		t.Fatalf("expired ring delivered: %v %v", r, err)
	}
}

func TestPollTimesOutEmpty(t *testing.T) {
	s, c := setup(t, Options{Wait: 50 * time.Millisecond})
	r, err := c.Poll(ctx(t), []ID{a})
	if err != nil || r != nil {
		t.Fatalf("got %v, %v", r, err)
	}
	if s.Held() != 0 {
		t.Fatal("a finished poll left state behind")
	}
}

func TestLimits(t *testing.T) {
	_, c := setup(t, Options{RingRate: rate.Every(time.Hour), RingBurst: 2})
	if err := c.Ring(ctx(t), a, bytes.Repeat([]byte{1}, MaxRing+1)); err == nil {
		t.Fatal("oversize ring accepted")
	}
	_ = c.Ring(ctx(t), a, []byte("1"))
	if err := c.Ring(ctx(t), a, []byte("2")); err == nil {
		t.Fatal("ring past the rate limit accepted")
	}
	if _, err := c.Poll(ctx(t), []ID{{1}, {2}, {3}, {4}, {5}}); err == nil {
		t.Fatal("poll on five inboxes accepted")
	}
}

func TestInboxHoldsAtMostEight(t *testing.T) {
	_, c := setup(t, Options{RingBurst: 100})
	for i := range perInbox {
		if err := c.Ring(ctx(t), a, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Ring(ctx(t), a, []byte("overflow")); err == nil {
		t.Fatal("ninth held ring accepted")
	}
}

func TestFromRelay(t *testing.T) {
	for in, want := range map[string]string{
		"wss://satchel-send.fly.dev/ws": "https://satchel-send.fly.dev",
		"ws://127.0.0.1:8080/ws":        "http://127.0.0.1:8080",
	} {
		if got, err := FromRelay(in); err != nil || got != want {
			t.Errorf("FromRelay(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := FromRelay("https://x"); err == nil {
		t.Error("accepted a non-websocket URL")
	}
}

func TestListenDeliversAndStops(t *testing.T) {
	_, c := setup(t, Options{Wait: 100 * time.Millisecond})
	lctx, cancel := context.WithCancel(context.Background())
	got := make(chan Ring, 4)
	done := make(chan struct{})
	go func() {
		c.Listen(lctx, func() []ID { return []ID{a} }, func(r Ring) { got <- r })
		close(done)
	}()
	_ = c.Ring(ctx(t), a, []byte("one"))
	select {
	case r := <-got:
		if string(r.Data) != "one" {
			t.Fatalf("got %q", r.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener never heard the ring")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not stop")
	}
}

// Off Fly, a forged Fly-Client-IP must not buy a fresh rate limit.
func TestForgedClientIPIgnoredUnlessTrusted(t *testing.T) {
	_, c := setup(t, Options{RingRate: rate.Every(time.Hour), RingBurst: 1})
	_ = c.Ring(ctx(t), a, []byte("1"))
	req, _ := http.NewRequest(http.MethodPost, c.Base+"/bell/"+a.String(), bytes.NewReader([]byte("2")))
	req.Header.Set("Fly-Client-IP", "203.0.113.9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("forged header escaped the limit: %s", resp.Status)
	}
}
