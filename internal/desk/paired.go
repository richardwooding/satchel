//go:build !js

package desk

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/richardwooding/satchel/internal/bell"
	"github.com/richardwooding/satchel/internal/manifest"
	"github.com/richardwooding/satchel/internal/pair"
	"github.com/richardwooding/satchel/internal/share"
	"github.com/richardwooding/satchel/internal/source"
	"github.com/richardwooding/satchel/internal/xfer"
)

// pairing is an open pairing session and whoever has introduced themselves.
type pairing struct {
	s          *share.Session
	started    time.Time
	candidates []pair.Candidate
}

// ErrNoPairing is returned when paired devices are turned off.
var ErrNoPairing = errors.New("paired devices are not available")

func (d *Desk) pairs() (*pair.Store, pair.Identity, error) {
	if d.cfg.Pairs == nil {
		return nil, pair.Identity{}, ErrNoPairing
	}
	id, err := d.cfg.Pairs.Identity()
	return d.cfg.Pairs, id, err
}

// PairStart opens a pairing session; read its phrase out to the other device.
func (d *Desk) PairStart(ctx context.Context) (string, error) {
	_, id, err := d.pairs()
	if err != nil {
		return "", err
	}
	s, err := share.Host(ctx, d.cfg.RelayURL, pair.NewService(id, d.cfg.DeviceName))
	if err != nil {
		return "", err
	}
	d.adoptPairing(s)
	return s.Phrase(), nil
}

// PairJoin enters another device's pairing session.
func (d *Desk) PairJoin(ctx context.Context, phrase string) error {
	_, id, err := d.pairs()
	if err != nil {
		return err
	}
	s, err := share.Join(ctx, d.cfg.RelayURL, strings.ToLower(strings.TrimSpace(phrase)), pair.NewService(id, d.cfg.DeviceName))
	if err != nil {
		return err
	}
	d.adoptPairing(s)
	return nil
}

func (d *Desk) adoptPairing(s *share.Session) {
	p := &pairing{s: s, started: time.Now()}
	phrase := s.Phrase()
	d.mu.Lock()
	d.pairings[phrase] = p
	d.mu.Unlock()
	d.changed()
	time.AfterFunc(10*time.Minute, func() { d.PairCancel(phrase) })
	go s.Run(func(ev any) {
		switch e := ev.(type) {
		case pair.Candidate:
			d.mu.Lock()
			p.candidates = append(p.candidates, e)
			d.mu.Unlock()
		case share.Closed:
			d.mu.Lock()
			if d.pairings[phrase] == p {
				delete(d.pairings, phrase)
			}
			d.mu.Unlock()
		default:
			return
		}
		d.changed()
	})
}

// PairConfirm saves a candidate once the person has seen the same check code
// on both screens, and closes the pairing session.
func (d *Desk) PairConfirm(phrase, peerID string) error {
	store, _, err := d.pairs()
	if err != nil {
		return err
	}
	d.mu.Lock()
	p := d.pairings[phrase]
	var found *pair.Candidate
	if p != nil {
		for i := range p.candidates {
			if p.candidates[i].Peer.ID == peerID {
				found = &p.candidates[i]
			}
		}
	}
	d.mu.Unlock()
	if found == nil {
		return errors.New("that pairing is no longer open")
	}
	peer := found.Peer
	peer.Paired = time.Now()
	if err := store.Add(peer); err != nil {
		return err
	}
	d.PairCancel(phrase)
	d.StartBell()
	d.cfg.Notify(Note{Kind: "paired", Title: "Paired with " + peer.Name, Body: "Send to it from the tray, no phrase needed."})
	return nil
}

// PairCancel closes a pairing session without saving anyone.
func (d *Desk) PairCancel(phrase string) {
	d.mu.Lock()
	p := d.pairings[phrase]
	delete(d.pairings, phrase)
	d.mu.Unlock()
	if p != nil {
		p.s.Close()
	}
	d.changed()
}

// Unpair forgets a device. It can no longer ring this one or be sent to.
func (d *Desk) Unpair(peerID string) error {
	store, _, err := d.pairs()
	if err != nil {
		return err
	}
	if err := store.Remove(peerID); err != nil {
		return err
	}
	d.changed()
	return nil
}

func (d *Desk) peer(peerID string) (pair.Peer, error) {
	store, _, err := d.pairs()
	if err != nil {
		return pair.Peer{}, err
	}
	p, ok := store.Peer(peerID)
	if !ok {
		return pair.Peer{}, errors.New("that device is not paired")
	}
	return p, nil
}

// ShareClipboardTo sends the clipboard to a paired device.
func (d *Desk) ShareClipboardTo(ctx context.Context, peerID string) error {
	p, err := d.peer(peerID)
	if err != nil {
		return err
	}
	c, err := d.cfg.Clipboard.Read()
	if err != nil {
		return fmt.Errorf("couldn't read the clipboard: %w", err)
	}
	if c.Empty() {
		return errors.New("the clipboard is empty")
	}
	if c.PNG != nil {
		it := xfer.ItemFor(stamp("clipboard", ".png"), manifest.KindImage, "image/png", c.PNG)
		_, err = d.shareVia(ctx, &p, "clipboard image", manifest.Manifest{Items: []manifest.Item{it}}, xfer.Bytes{c.PNG}, nil)
		return err
	}
	data := []byte(c.Text)
	it := xfer.ItemFor("text.txt", manifest.KindText, "text/plain;charset=utf-8", data)
	_, err = d.shareVia(ctx, &p, "clipboard text", manifest.Manifest{Items: []manifest.Item{it}}, xfer.Bytes{data}, nil)
	return err
}

// ShareFilesTo sends files and folders to a paired device.
func (d *Desk) ShareFilesTo(ctx context.Context, peerID string, paths []string, progress source.Progress) error {
	p, err := d.peer(peerID)
	if err != nil {
		return err
	}
	b, err := source.Build(paths, progress)
	if err != nil {
		return err
	}
	title := b.Manifest.Items[0].Path
	if top := topLevel(b.Manifest); len(top) > 1 {
		title = fmt.Sprintf("%s and %d more", top[0], len(top)-1)
	}
	if _, err := d.shareVia(ctx, &p, title, b.Manifest, b.Files, b.Files); err != nil {
		_ = b.Files.Close()
		return err
	}
	return nil
}

// hostFor hosts a rendezvous with a paired device and rings it.
func (d *Desk) hostFor(ctx context.Context, to pair.Peer) (*share.Session, error) {
	_, id, err := d.pairs()
	if err != nil {
		return nil, err
	}
	secret, err := id.Secret(to.Pub)
	if err != nil {
		return nil, err
	}
	ring, phrase, err := pair.NewRing(secret)
	if err != nil {
		return nil, err
	}
	s, err := share.HostPhrase(ctx, d.cfg.RelayURL, phrase)
	if err != nil {
		return nil, err
	}
	b, err := d.bellClient()
	if err == nil {
		err = b.Ring(ctx, pair.InboxID(to.Inbox, time.Now()), ring)
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("couldn't reach %s: %w", to.Name, err)
	}
	return s, nil
}

func (d *Desk) bellClient() (*bell.Client, error) {
	base, err := bell.FromRelay(d.cfg.RelayURL)
	if err != nil {
		return nil, err
	}
	return &bell.Client{Base: base}, nil
}

// StartBell listens for paired devices, if there are any and it is not
// already listening. It is cheap to call again.
func (d *Desk) StartBell() {
	store, id, err := d.pairs()
	if err != nil || len(store.Peers()) == 0 {
		return
	}
	b, err := d.bellClient()
	if err != nil {
		return
	}
	d.mu.Lock()
	if d.bellStop != nil || d.closed {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.bellStop = cancel
	d.mu.Unlock()
	go b.Listen(ctx, func() []bell.ID { return pair.Listening(id.Inbox, time.Now()) }, func(r bell.Ring) {
		d.onRing(ctx, id, r.Data)
	})
}

// onRing joins the rendezvous a paired device rang about. A ring that
// matches no paired device is ignored: it costs a hash per peer, nothing
// more — no connection is made.
func (d *Desk) onRing(ctx context.Context, id pair.Identity, data []byte) {
	peer, phrase, ok := pair.Match(id, d.cfg.Pairs.Peers(), data)
	if !ok {
		return
	}
	jctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s, err := share.Join(jctx, d.cfg.RelayURL, phrase)
	if err != nil {
		log.Printf("desk: join %s's rendezvous: %v", peer.Name, err)
		return
	}
	d.adopt(&sess{s: s, role: roleReceive, title: "from " + peer.Name, started: time.Now(),
		status: "connected", peer: peer.Name, ask: true})
}

// Accept takes an offer that is waiting for an answer.
func (d *Desk) Accept(phrase string, from uint32, offerID string) error {
	d.mu.Lock()
	ss, o := d.pending(phrase, from, offerID)
	var run func()
	if o != nil {
		ss.status = "receiving"
		run = d.accept(ss, o)
	}
	d.mu.Unlock()
	if o == nil {
		return errors.New("that offer is gone")
	}
	if run != nil {
		run()
	}
	d.changed()
	return nil
}

// Decline refuses an offer; the sender's share stays open until it stops.
func (d *Desk) Decline(phrase string, from uint32, offerID string) {
	d.mu.Lock()
	ss, o := d.pending(phrase, from, offerID)
	if o != nil {
		o.status = "declined"
		ss.s.X.Decline(o.from, o.id)
	}
	d.mu.Unlock()
	if o != nil {
		d.Stop(phrase)
	}
}

// pending finds an offer still waiting for an answer. Called with mu held.
func (d *Desk) pending(phrase string, from uint32, offerID string) (*sess, *offer) {
	ss := d.sessions[phrase]
	if ss == nil {
		return nil, nil
	}
	for _, o := range ss.offers {
		if uint32(o.from) == from && o.id.String() == offerID && o.status == "offered" {
			return ss, o
		}
	}
	return nil, nil
}

func describe(s xfer.Summary) string {
	if s.Items == 1 {
		return human(s.Bytes)
	}
	return fmt.Sprintf("%d items · %s", s.Items, human(s.Bytes))
}
