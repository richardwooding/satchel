//go:build !js

package desk

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/richardwooding/parley/relay"

	"github.com/richardwooding/satchel/internal/bell"
	"github.com/richardwooding/satchel/internal/pair"
)

// dials counts every connection attempt to the relay.
var dials atomic.Int64

// server runs the relay and the bell together, as satchel-relay does.
func server(t *testing.T) string {
	t.Helper()
	rs := relay.New(relay.Options{Grace: 5 * time.Second, ConnRate: rate.Inf})
	t.Cleanup(rs.Close)
	bs := bell.New(bell.Options{Wait: 2 * time.Second, RingBurst: 100})
	mux := http.NewServeMux()
	mux.Handle("/ws", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dials.Add(1)
		rs.ServeHTTP(w, r)
	}))
	mux.Handle("/bell", bs)
	mux.Handle("/bell/", bs)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

func pairedRig(t *testing.T, url, name string) *rig {
	t.Helper()
	dir := t.TempDir()
	store, err := pair.Open(dir, pair.FileSecrets(filepath.Join(dir, "secrets")))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{clip: &fakeClip{}, dl: t.TempDir(), notes: make(chan Note, 64)}
	r.d = New(Config{RelayURL: url, Downloads: r.dl, Clipboard: r.clip, Pairs: store, DeviceName: name,
		Notify: func(n Note) { r.notes <- n }})
	t.Cleanup(r.d.Close)
	return r
}

// pairUp pairs a and b the way two people would: a opens, b joins, both
// read the same code and confirm.
func pairUp(t *testing.T, a, b *rig) {
	t.Helper()
	phrase, err := a.d.PairStart(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.d.PairJoin(ctx(t), phrase); err != nil {
		t.Fatal(err)
	}
	ca, cb := waitCandidate(t, a.d), waitCandidate(t, b.d)
	if ca.Code != cb.Code {
		t.Fatalf("check codes differ: %s / %s", ca.Code, cb.Code)
	}
	if err := b.d.PairConfirm(phrase, cb.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.d.PairConfirm(phrase, ca.ID); err != nil {
		t.Fatal(err)
	}
	a.note(t, "paired")
	b.note(t, "paired")
}

func waitCandidate(t *testing.T, d *Desk) Candidate {
	t.Helper()
	var c Candidate
	waitFor(t, func() bool {
		for _, p := range d.State().Pairings {
			if len(p.Candidates) > 0 {
				c = p.Candidates[0]
				return true
			}
		}
		return false
	})
	return c
}

func peerID(t *testing.T, d *Desk, name string) string {
	t.Helper()
	for _, p := range d.State().Peers {
		if p.Name == name {
			return p.ID
		}
	}
	t.Fatalf("%s is not paired", name)
	return ""
}

func TestPairThenSendWithoutAPhrase(t *testing.T) {
	url := server(t)
	desktop, phone := pairedRig(t, url, "desktop"), pairedRig(t, url, "phone")
	pairUp(t, desktop, phone)
	if n := len(desktop.d.State().Pairings); n != 0 {
		t.Fatalf("%d pairing sessions left open", n)
	}

	desktop.clip.text = "sent by name, no phrase"
	if err := desktop.d.ShareClipboardTo(ctx(t), peerID(t, desktop.d, "phone")); err != nil {
		t.Fatal(err)
	}
	// Paired offers ask first.
	n := phone.note(t, "offer")
	if !strings.Contains(n.Title, "desktop") {
		t.Fatalf("offer note %q", n.Title)
	}
	if got, _ := phone.clip.Read(); got.Text != "" {
		t.Fatal("offer taken before it was accepted")
	}
	if err := phone.d.Accept(n.Phrase, n.From, n.OfferID); err != nil {
		t.Fatal(err)
	}
	phone.note(t, "received")
	if got, _ := phone.clip.Read(); got.Text != "sent by name, no phrase" {
		t.Fatalf("phone clipboard %q", got.Text)
	}
	waitFor(t, func() bool {
		s := desktop.d.State().Shares
		return len(s) == 1 && s[0].Done == 1 && s[0].Peer == "phone"
	})
}

func TestDeclineTakesNothing(t *testing.T) {
	url := server(t)
	a, b := pairedRig(t, url, "a"), pairedRig(t, url, "b")
	pairUp(t, a, b)
	a.clip.text = "unwanted"
	if err := a.d.ShareClipboardTo(ctx(t), peerID(t, a.d, "b")); err != nil {
		t.Fatal(err)
	}
	n := b.note(t, "offer")
	b.d.Decline(n.Phrase, n.From, n.OfferID)
	if err := b.d.Accept(n.Phrase, n.From, n.OfferID); err == nil {
		t.Fatal("accepted an offer after declining it")
	}
	if got, _ := b.clip.Read(); got.Text != "" {
		t.Fatal("declined text arrived anyway")
	}
}

// A ring from a device that is not paired — or random bytes — costs the
// receiver nothing: not a session, and not even a connection attempt. That
// is the hint's job; without it every junk ring would spend one of the
// receiver's five relay connections a minute.
func TestForgedRingIsIgnored(t *testing.T) {
	url := server(t)
	a, b := pairedRig(t, url, "a"), pairedRig(t, url, "b")
	pairUp(t, a, b)
	time.Sleep(200 * time.Millisecond) // let the pairing sessions finish closing
	before := dials.Load()
	id, _ := b.d.cfg.Pairs.Identity()
	base, _ := bell.FromRelay(url)
	junk := make([]byte, pair.RingSize)
	_, _ = rand.Read(junk)
	if err := (&bell.Client{Base: base}).Ring(context.Background(), pair.InboxID(id.Inbox, time.Now()), junk); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if n := len(b.d.State().Receives); n != 0 {
		t.Fatalf("a forged ring opened %d sessions", n)
	}
	if n := dials.Load() - before; n != 0 {
		t.Fatalf("a forged ring cost %d relay connection attempts", n)
	}
}

func TestUnpairedDeviceCannotReach(t *testing.T) {
	url := server(t)
	a, b := pairedRig(t, url, "a"), pairedRig(t, url, "b")
	pairUp(t, a, b)
	if err := b.d.Unpair(peerID(t, b.d, "a")); err != nil {
		t.Fatal(err)
	}
	a.clip.text = "after unpairing"
	if err := a.d.ShareClipboardTo(ctx(t), peerID(t, a.d, "b")); err != nil {
		t.Fatal(err) // the ring is posted; b just no longer answers it
	}
	time.Sleep(500 * time.Millisecond)
	if n := len(b.d.State().Receives); n != 0 {
		t.Fatalf("an unpaired device's ring opened %d sessions", n)
	}
}
