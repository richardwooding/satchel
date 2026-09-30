package xfer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/richardwooding/satchel/internal/manifest"
	"github.com/richardwooding/satchel/internal/sink"
)

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// Clipboard text is small enough to ride inside the offer: accepting needs no
// pull at all.
func TestClipboardTextInline(t *testing.T) {
	url := relayURL(t)
	a, phrase := host(t, url)
	b := join(t, url, phrase)

	text := []byte("the quick brown fox · ünïcødé")
	m := manifest.Manifest{Items: []manifest.Item{ItemFor("clipboard.txt", manifest.KindText, "text/plain", text)}}
	if _, err := a.x.Offer(m, Bytes{text}); err != nil {
		t.Fatal(err)
	}
	off := await[Offered](t, b, 5*time.Second)
	if off.Summary.Kind != manifest.KindText || off.Summary.Bytes != int64(len(text)) {
		t.Fatalf("summary %+v", off.Summary)
	}
	mem := sink.NewMemory(1 << 20)
	if err := b.x.Accept(off.From, off.ID, mem, manifest.Default); err != nil {
		t.Fatal(err)
	}
	await[Received](t, b, 5*time.Second)
	if got, _ := mem.Item(0); !bytes.Equal(got, text) {
		t.Fatalf("got %q", got)
	}
	if d := await[Delivered](t, a, 5*time.Second); d.To != b.c.Self() {
		t.Fatalf("delivered to %d", d.To)
	}
}

// slowSink wraps a sink so every write takes a while: the receiver falls far
// behind a sender that could saturate the link, which is exactly the case in
// which a push design would get it kicked by the relay.
//
// Measured 2026-09-30, by raising Window and rerunning
// TestSlowReceiverLargeFileIsNeverDropped: 128 chunks in flight still
// passes, 512 gets the receiver dropped. The slack is more than the relay's
// 64-frame buffer because the receiving client drains its socket into a
// 256-event channel and localhost socket buffers hold megabytes more — so
// Window = 16 has roughly an order of magnitude of margin, not four.
type slowSink struct {
	sink.Sink
	delay time.Duration
}

func (s slowSink) Create(i int) (sink.ItemWriter, error) {
	w, err := s.Sink.Create(i)
	if err != nil {
		return nil, err
	}
	return slowWriter{w, s.delay}, nil
}

type slowWriter struct {
	sink.ItemWriter
	delay time.Duration
}

func (w slowWriter) Write(p []byte) (int, error) {
	time.Sleep(w.delay)
	return w.ItemWriter.Write(p)
}

func TestSlowReceiverLargeFileIsNeverDropped(t *testing.T) {
	if testing.Short() {
		t.Skip("moves 64 MiB")
	}
	url := relayURL(t)
	a, phrase := host(t, url)
	b := join(t, url, phrase)

	data := randBytes(t, 64<<20+12345) // not a chunk multiple
	sum := sha256.Sum256(data)
	m := manifest.Manifest{Items: []manifest.Item{{Path: "big.bin", Kind: manifest.KindFile, Size: int64(len(data)), SHA256: sum[:]}}}
	if _, err := a.x.Offer(m, Bytes{data}); err != nil {
		t.Fatal(err)
	}
	off := await[Offered](t, b, 5*time.Second)
	dest := t.TempDir()
	d, err := sink.NewDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	// ~1370 chunks at 2ms each: the receiver is the bottleneck throughout.
	if err := b.x.Accept(off.From, off.ID, slowSink{d, 2 * time.Millisecond}, manifest.Default); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	await[Received](t, b, 45*time.Second)
	got, err := os.ReadFile(filepath.Join(dest, "big.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("file differs (err %v)", err)
	}
	t.Logf("64 MiB through a throttled receiver in %v", time.Since(start).Round(time.Millisecond))
}

// A receiver whose connection drops mid-transfer reconnects, rebinds, and
// finishes from where it was: Rebind re-Attaches the service, which re-pulls
// from the last byte received (the stall timer is only the fallback).
func TestResumeAfterDrop(t *testing.T) {
	url := relayURL(t)
	a, phrase := host(t, url)
	b := join(t, url, phrase)

	data := randBytes(t, 8<<20)
	sum := sha256.Sum256(data)
	m := manifest.Manifest{Items: []manifest.Item{{Path: "f", Kind: manifest.KindFile, Size: int64(len(data)), SHA256: sum[:]}}}
	if _, err := a.x.Offer(m, Bytes{data}); err != nil {
		t.Fatal(err)
	}
	off := await[Offered](t, b, 5*time.Second)
	mem := sink.NewMemory(16 << 20)
	if err := b.x.Accept(off.From, off.ID, slowSink{mem, time.Millisecond}, manifest.Default); err != nil {
		t.Fatal(err)
	}
	p := await[Progress](t, b, 10*time.Second)
	if p.Done >= p.Total {
		t.Fatal("finished before the drop; nothing tested")
	}

	_ = b.c.CloseNow()
	time.Sleep(200 * time.Millisecond)
	if err := b.c.Reconnect(testCtx(t)); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	b.mux.Rebind(b.c)

	await[Received](t, b, 40*time.Second)
	if got, _ := mem.Item(0); !bytes.Equal(got, data) {
		t.Fatal("resumed file differs")
	}
}

// An offer made before anyone joined reaches a later joiner through hello,
// even though the sender is not the one the joiner asked.
func TestLateJoinerSeesStandingOffer(t *testing.T) {
	url := relayURL(t)
	a, phrase := host(t, url)
	text := []byte("waiting for you")
	m := manifest.Manifest{Items: []manifest.Item{ItemFor("note.txt", manifest.KindText, "text/plain", text)}}
	if _, err := a.x.Offer(m, Bytes{text}); err != nil {
		t.Fatal(err)
	}
	b := join(t, url, phrase)
	off := await[Offered](t, b, 5*time.Second)
	if off.Summary.Name != "note.txt" {
		t.Fatalf("summary %+v", off.Summary)
	}

	// A joiner that is not the host can offer too, and the host receives it.
	c := join(t, url, phrase)
	await[Offered](t, c, 5*time.Second) // a's standing offer
	back := []byte("from the phone")
	if _, err := c.x.Offer(manifest.Manifest{Items: []manifest.Item{ItemFor("p.txt", manifest.KindText, "", back)}}, Bytes{back}); err != nil {
		t.Fatal(err)
	}
	if got := await[Offered](t, a, 5*time.Second); got.From != c.c.Self() {
		t.Fatalf("host saw an offer from %d", got.From)
	}
}

// A folder too big to describe inline: the manifest itself is pulled and
// verified, then two receivers pull the items independently.
func TestLargeFolderTwoReceivers(t *testing.T) {
	url := relayURL(t)
	a, phrase := host(t, url)
	b := join(t, url, phrase)
	c := join(t, url, phrase)

	var items []manifest.Item
	var data [][]byte
	items = append(items, manifest.Item{Path: "tree", Kind: manifest.KindDir})
	data = append(data, nil)
	for i := range 1500 {
		body := randBytes(t, i%3*700) // includes empty files
		sum := sha256.Sum256(body)
		items = append(items, manifest.Item{
			Path: fmt.Sprintf("tree/d%02d/file-%04d.dat", i%40, i), Kind: manifest.KindFile,
			Size: int64(len(body)), SHA256: sum[:],
		})
		data = append(data, body)
	}
	m := manifest.Manifest{Items: items}
	if raw, _ := m.Encode(); len(raw) <= inlineManifestMax {
		t.Fatalf("manifest is only %d bytes; the pull path is not exercised", len(raw))
	}
	if _, err := a.x.Offer(m, Bytes(data)); err != nil {
		t.Fatal(err)
	}
	dests := map[*peer]string{}
	for _, r := range []*peer{b, c} {
		off := await[Offered](t, r, 5*time.Second)
		dests[r] = t.TempDir()
		d, _ := sink.NewDir(dests[r])
		if err := r.x.Accept(off.From, off.ID, d, manifest.Default); err != nil {
			t.Fatal(err)
		}
	}
	for r, dest := range dests {
		await[Received](t, r, 30*time.Second)
		for i := 1; i < len(items); i += 97 {
			got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(items[i].Path)))
			if err != nil || !bytes.Equal(got, data[i]) {
				t.Fatalf("%s differs (err %v)", items[i].Path, err)
			}
		}
	}
}

// A sender that lies about an item's contents gets nothing committed.
func TestCorruptItemIsNotCommitted(t *testing.T) {
	url := relayURL(t)
	a, phrase := host(t, url)
	b := join(t, url, phrase)

	real := randBytes(t, 200_000)
	sum := sha256.Sum256(real)
	m := manifest.Manifest{Items: []manifest.Item{{Path: "x.bin", Kind: manifest.KindFile, Size: int64(len(real)), SHA256: sum[:]}}}
	lie := bytes.Clone(real)
	lie[150_000] ^= 1
	if _, err := a.x.Offer(m, Bytes{lie}); err != nil {
		t.Fatal(err)
	}
	off := await[Offered](t, b, 5*time.Second)
	dest := t.TempDir()
	d, _ := sink.NewDir(dest)
	if err := b.x.Accept(off.From, off.ID, d, manifest.Default); err != nil {
		t.Fatal(err)
	}
	f := await[Failed](t, b, 10*time.Second)
	if f.Err == nil {
		t.Fatal("failed without a reason")
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("corrupt item left files: %v", entries)
	}
}

func TestWithdrawMidTransfer(t *testing.T) {
	url := relayURL(t)
	a, phrase := host(t, url)
	b := join(t, url, phrase)

	data := randBytes(t, 8<<20)
	sum := sha256.Sum256(data)
	m := manifest.Manifest{Items: []manifest.Item{{Path: "f", Kind: manifest.KindFile, Size: int64(len(data)), SHA256: sum[:]}}}
	id, err := a.x.Offer(m, Bytes{data})
	if err != nil {
		t.Fatal(err)
	}
	off := await[Offered](t, b, 5*time.Second)
	if err := b.x.Accept(off.From, off.ID, slowSink{sink.NewMemory(16 << 20), 2 * time.Millisecond}, manifest.Default); err != nil {
		t.Fatal(err)
	}
	await[Progress](t, b, 10*time.Second)
	if err := a.x.Withdraw(id); err != nil {
		t.Fatal(err)
	}
	if f := await[Failed](t, b, 10*time.Second); !errors.Is(f.Err, ErrWithdrawn) {
		t.Fatalf("failed with %v, want ErrWithdrawn", f.Err)
	}
}

// A hostile sender skips Offer's own validation and writes frames by hand.
// Nothing it sends may reach the filesystem outside the destination.
func TestHostileOfferFrames(t *testing.T) {
	escape := manifest.Manifest{Items: []manifest.Item{ItemFor("../escaped.txt", manifest.KindFile, "", []byte("pwn"))}}
	liar := manifest.Manifest{Items: []manifest.Item{ItemFor("small.txt", manifest.KindFile, "", []byte("pwn"))}}
	cases := map[string]struct {
		m       manifest.Manifest
		summary Summary
	}{
		"path escape":   {escape, Summary{Items: 1, Bytes: 3, Name: "escaped.txt", Kind: manifest.KindFile}},
		"lying summary": {liar, Summary{Items: 1, Bytes: 1, Name: "tiny", Kind: manifest.KindFile}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			url := relayURL(t)
			a, phrase := host(t, url)
			b := join(t, url, phrase)

			raw, err := tc.m.Encode()
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			id, _ := newOfferID()
			frame := msg{
				Kind: kindOffer, ID: id[:], Summary: &tc.summary,
				Manifest: raw, ManifestSize: int64(len(raw)), ManifestSHA: sum[:], Inline: []byte("pwn"),
			}
			if err := a.x.sendTo(b.c.Self(), frame); err != nil {
				t.Fatal(err)
			}
			off := await[Offered](t, b, 5*time.Second)
			parent := t.TempDir()
			dest := filepath.Join(parent, "inbox")
			d, err := sink.NewDir(dest)
			if err != nil {
				t.Fatal(err)
			}
			_ = b.x.Accept(off.From, off.ID, d, manifest.Default)
			if f := await[Failed](t, b, 5*time.Second); !errors.Is(f.Err, manifest.ErrInvalid) && name == "path escape" {
				t.Fatalf("failed with %v, want a manifest rejection", f.Err)
			}
			for _, dir := range []string{parent, dest} {
				entries, _ := os.ReadDir(dir)
				for _, e := range entries {
					if e.Name() != "inbox" {
						t.Fatalf("hostile offer wrote %s/%s", dir, e.Name())
					}
				}
			}
		})
	}
}
