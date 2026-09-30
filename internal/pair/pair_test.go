//go:build !js

package pair

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/richardwooding/parley/relay"

	"github.com/richardwooding/satchel/internal/share"
)

func ident(t *testing.T) Identity {
	t.Helper()
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func peerOf(id Identity, name string) Peer {
	return Peer{ID: PeerID(id.Public()), Name: name, Pub: id.Public(), Inbox: id.Inbox}
}

func TestSecretIsSymmetricAndPairSpecific(t *testing.T) {
	a, b, c := ident(t), ident(t), ident(t)
	ab, _ := a.Secret(b.Public())
	ba, _ := b.Secret(a.Public())
	ac, _ := a.Secret(c.Public())
	if !bytes.Equal(ab, ba) {
		t.Fatal("the two ends derived different pair secrets")
	}
	if bytes.Equal(ab, ac) {
		t.Fatal("different peers share a secret")
	}
	if CheckCode(ab) != CheckCode(ba) || CheckCode(ab) == CheckCode(ac) {
		t.Fatal("check codes do not follow the secret")
	}
	if _, err := a.Secret(a.Public()); err == nil {
		t.Fatal("paired with itself")
	}
	if _, err := a.Secret([]byte("short")); err == nil {
		t.Fatal("accepted a malformed key")
	}
}

func TestRingMatchesOnlyTheRealPeer(t *testing.T) {
	me, alice, bob, eve := ident(t), ident(t), ident(t), ident(t)
	peers := []Peer{peerOf(alice, "alice"), peerOf(bob, "bob")}

	secret, _ := bob.Secret(me.Public())
	ring, phrase, err := NewRing(secret)
	if err != nil {
		t.Fatal(err)
	}
	p, got, ok := Match(me, peers, ring)
	if !ok || p.Name != "bob" || got != phrase || len(phrase) != 32 {
		t.Fatalf("matched %q/%q ok=%v, want bob/%q", p.Name, got, ok, phrase)
	}

	// Eve is not paired: her ring matches no one, however it is built.
	evil, _ := eve.Secret(me.Public())
	eveRing, _, _ := NewRing(evil)
	if _, _, ok := Match(me, peers, eveRing); ok {
		t.Fatal("an unpaired device's ring matched")
	}
	tampered := bytes.Clone(ring)
	tampered[3] ^= 1
	if _, _, ok := Match(me, peers, tampered); ok {
		t.Fatal("a tampered ring matched")
	}
	if _, _, ok := Match(me, peers, ring[:10]); ok {
		t.Fatal("a short ring matched")
	}

	// Two rings from the same peer share nothing an observer could link.
	ring2, phrase2, _ := NewRing(secret)
	if bytes.Equal(ring, ring2) || phrase == phrase2 {
		t.Fatal("rings repeat")
	}
}

func TestInboxRotatesDaily(t *testing.T) {
	id := ident(t)
	day := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if InboxID(id.Inbox, day) != InboxID(id.Inbox, day.Add(6*time.Hour)) {
		t.Fatal("inbox changed within a day")
	}
	if InboxID(id.Inbox, day) == InboxID(id.Inbox, day.Add(24*time.Hour)) {
		t.Fatal("inbox did not rotate")
	}
	if n := len(Listening(id.Inbox, day)); n != 1 {
		t.Fatalf("midday listens on %d inboxes", n)
	}
	justAfter := time.Date(2026, 10, 1, 0, 3, 0, 0, time.UTC)
	if got := Listening(id.Inbox, justAfter); len(got) != 2 || got[1] != InboxID(id.Inbox, day) {
		t.Fatal("just after midnight should also hear yesterday's inbox")
	}
	justBefore := time.Date(2026, 9, 30, 23, 55, 0, 0, time.UTC)
	if got := Listening(id.Inbox, justBefore); len(got) != 2 || got[1] != InboxID(id.Inbox, justAfter) {
		t.Fatal("just before midnight should also hear tomorrow's inbox")
	}
}

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, FileSecrets(filepath.Join(dir, "secrets")))
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Identity()
	if err != nil {
		t.Fatal(err)
	}
	other := ident(t)
	if err := s.Add(peerOf(other, "laptop")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "peers.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("peers.json is %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(dir, "secrets", "identity.secret")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("identity secret is %v", fi.Mode().Perm())
	}

	s2, err := Open(dir, FileSecrets(filepath.Join(dir, "secrets")))
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := s2.Identity()
	if !bytes.Equal(id.Public(), id2.Public()) || !bytes.Equal(id.Inbox, id2.Inbox) {
		t.Fatal("identity changed across restarts")
	}
	if ps := s2.Peers(); len(ps) != 1 || ps[0].Name != "laptop" {
		t.Fatalf("peers %+v", ps)
	}
	_ = s2.Remove(PeerID(other.Public()))
	if len(s2.Peers()) != 0 {
		t.Fatal("remove did not stick")
	}
}

// Pairing over a real relay: both ends see each other, with the same code.
func TestPairingSession(t *testing.T) {
	rs := relay.New(relay.Options{ConnRate: rate.Inf})
	t.Cleanup(rs.Close)
	srv := httptest.NewServer(rs)
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a, b := ident(t), ident(t)
	host, err := share.Host(ctx, url, NewService(a, "desktop"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	hostEv := make(chan any, 64)
	go host.Run(func(e any) { hostEv <- e })
	joiner, err := share.Join(ctx, url, host.Phrase(), NewService(b, "  phone\x07 "))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(joiner.Close)
	joinEv := make(chan any, 64)
	go joiner.Run(func(e any) { joinEv <- e })

	ca, cb := candidate(t, hostEv), candidate(t, joinEv)
	if ca.Code != cb.Code {
		t.Fatalf("codes differ: %s vs %s", ca.Code, cb.Code)
	}
	if ca.Peer.Name != "phone" || cb.Peer.Name != "desktop" {
		t.Fatalf("names %q / %q", ca.Peer.Name, cb.Peer.Name)
	}
	if !bytes.Equal(ca.Peer.Pub, b.Public()) || !bytes.Equal(cb.Peer.Inbox, a.Inbox) {
		t.Fatal("wrong identity carried")
	}
}

func candidate(t *testing.T, ch <-chan any) Candidate {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-ch:
			if c, ok := e.(Candidate); ok {
				return c
			}
		case <-deadline:
			t.Fatal("no candidate")
		}
	}
}
