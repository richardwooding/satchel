//go:build !js

// Package desk is the tray app's brain, with no UI in it: the shares and
// receives in flight, what arrives where, and a State snapshot the window and
// the tray menu render. The Wails layer only calls it and draws it.
//
// Consent is explicit and simple. Starting a receive — typing a phrase, or
// opening a fresh phrase for someone to send to — is the person asking for
// what that session brings, so its offers are accepted as they arrive: text
// goes onto the clipboard, images and files into the downloads folder, never
// overwriting.
package desk

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/richardwooding/parley/wire"

	"github.com/richardwooding/satchel/internal/clip"
	"github.com/richardwooding/satchel/internal/manifest"
	"github.com/richardwooding/satchel/internal/pair"
	"github.com/richardwooding/satchel/internal/share"
	"github.com/richardwooding/satchel/internal/sink"
	"github.com/richardwooding/satchel/internal/source"
	"github.com/richardwooding/satchel/internal/xfer"
)

// DefaultRelay is the hosted relay.
const DefaultRelay = "wss://satchel-send.fly.dev/ws"

// ShareLifetime closes a share nobody stopped. Sessions live in the relay's
// memory; an hour is long enough to read a phrase out and short enough that
// a forgotten share does not linger.
const ShareLifetime = time.Hour

// memoryMax is the largest text or image kept in memory for the clipboard.
const memoryMax = 32 << 20

// Clipboard is what desk needs from internal/clip.
type Clipboard interface {
	Read() (clip.Content, error)
	WriteText(string) error
	WritePNG([]byte) error
}

// Note is something worth a desktop notification.
type Note struct {
	Kind   string // "share-ready", "received", "failed", "offer", "paired"
	Title  string
	Body   string
	Folder string // for "received": where the files are, if any
	// For "offer": what Accept or Decline needs.
	Phrase  string
	From    uint32
	OfferID string
}

// Config wires desk to the outside world. Changed and Notify may be called
// from any goroutine and must not block.
type Config struct {
	RelayURL  string
	Downloads string
	Clipboard Clipboard
	Changed   func(State)
	Notify    func(Note)
	// Pairs enables paired devices; nil turns them off.
	Pairs      *pair.Store
	DeviceName string
}

// Desk runs every session the tray app has open.
type Desk struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]*sess // by phrase
	pairings map[string]*pairing
	closed   bool
	bellStop func()
}

type role int

const (
	roleShare role = iota
	roleReceive
)

type sess struct {
	s       *share.Session
	role    role
	title   string
	items   int
	bytes   int64
	started time.Time
	peers   int
	done    int // shares: receivers that reported delivery
	status  string
	offers  []*offer
	files   *source.Files
	timer   *time.Timer
	peer    string // a paired device's name, for sessions with one
	ask     bool   // offers wait for Accept: they came from a paired device
}

type offer struct {
	from   wire.ParticipantID
	id     xfer.OfferID
	sum    xfer.Summary
	status string
	done   int64
	err    string
	placed string // folder the items went to
	text   string // preview of received text
	mem    *sink.Memory
	dir    *sink.Dir
}

// New returns a Desk; it opens no connection until asked.
func New(cfg Config) *Desk {
	if cfg.RelayURL == "" {
		cfg.RelayURL = DefaultRelay
	}
	if cfg.Changed == nil {
		cfg.Changed = func(State) {}
	}
	if cfg.Notify == nil {
		cfg.Notify = func(Note) {}
	}
	return &Desk{cfg: cfg, sessions: map[string]*sess{}, pairings: map[string]*pairing{}}
}

// Link is the browser URL for a phrase on this relay.
func (d *Desk) Link(phrase string) string {
	u, err := url.Parse(d.cfg.RelayURL)
	if err != nil {
		return phrase
	}
	scheme := "https"
	if u.Scheme == "ws" {
		scheme = "http"
	}
	return scheme + "://" + u.Host + "/#" + phrase
}

// ShareClipboard offers whatever is on the clipboard now.
func (d *Desk) ShareClipboard(ctx context.Context) (string, error) {
	c, err := d.cfg.Clipboard.Read()
	if err != nil {
		return "", fmt.Errorf("couldn't read the clipboard: %w", err)
	}
	if c.Empty() {
		return "", errors.New("the clipboard is empty")
	}
	if c.PNG != nil {
		it := xfer.ItemFor(stamp("clipboard", ".png"), manifest.KindImage, "image/png", c.PNG)
		return d.share(ctx, "clipboard image", manifest.Manifest{Items: []manifest.Item{it}}, xfer.Bytes{c.PNG}, nil)
	}
	return d.ShareText(ctx, c.Text)
}

// ShareText offers a piece of text.
func (d *Desk) ShareText(ctx context.Context, text string) (string, error) {
	data := []byte(text)
	it := xfer.ItemFor("text.txt", manifest.KindText, "text/plain;charset=utf-8", data)
	return d.share(ctx, "clipboard text", manifest.Manifest{Items: []manifest.Item{it}}, xfer.Bytes{data}, nil)
}

// ShareFiles offers files and folders from disk.
func (d *Desk) ShareFiles(ctx context.Context, paths []string, progress source.Progress) (string, error) {
	b, err := source.Build(paths, progress)
	if err != nil {
		return "", err
	}
	title := b.Manifest.Items[0].Path
	if top := topLevel(b.Manifest); len(top) > 1 {
		title = fmt.Sprintf("%s and %d more", top[0], len(top)-1)
	}
	phrase, err := d.share(ctx, title, b.Manifest, b.Files, b.Files)
	if err != nil {
		_ = b.Files.Close()
	}
	return phrase, err
}

func (d *Desk) share(ctx context.Context, title string, m manifest.Manifest, src xfer.Source, files *source.Files) (string, error) {
	return d.shareVia(ctx, nil, title, m, src, files)
}

// shareVia hosts an offer: on a fresh phrase, or — with to set — on a
// rendezvous with a paired device, which is then rung.
func (d *Desk) shareVia(ctx context.Context, to *pair.Peer, title string, m manifest.Manifest, src xfer.Source, files *source.Files) (string, error) {
	var s *share.Session
	var err error
	if to == nil {
		s, err = share.Host(ctx, d.cfg.RelayURL)
	} else {
		s, err = d.hostFor(ctx, *to)
	}
	if err != nil {
		return "", err
	}
	if _, err := s.X.Offer(m, src); err != nil {
		s.Close()
		return "", err
	}
	ss := &sess{s: s, role: roleShare, title: title, items: len(m.Items), bytes: m.Total(),
		started: time.Now(), status: "waiting", files: files}
	if to != nil {
		ss.peer = to.Name
		ss.status = "ringing"
	}
	d.adopt(ss)
	if to == nil {
		d.cfg.Notify(Note{Kind: "share-ready", Title: "Ready: " + s.Phrase(), Body: title + " · " + human(m.Total())})
	}
	return s.Phrase(), nil
}

// Receive joins a phrase someone gave you and takes what it offers.
func (d *Desk) Receive(ctx context.Context, phrase string) error {
	phrase = strings.ToLower(strings.TrimSpace(phrase))
	if phrase == "" {
		return errors.New("enter a code phrase")
	}
	if d.has(phrase) {
		return nil // already in it
	}
	s, err := share.Join(ctx, d.cfg.RelayURL, phrase)
	if err != nil {
		return err
	}
	d.adopt(&sess{s: s, role: roleReceive, title: "receiving", started: time.Now(), status: "connected"})
	return nil
}

// Open starts a fresh phrase for someone else — a phone, say — to send to.
func (d *Desk) Open(ctx context.Context) (string, error) {
	s, err := share.Host(ctx, d.cfg.RelayURL)
	if err != nil {
		return "", err
	}
	d.adopt(&sess{s: s, role: roleReceive, title: "waiting for a sender", started: time.Now(), status: "waiting"})
	return s.Phrase(), nil
}

// Stop closes a session by phrase.
func (d *Desk) Stop(phrase string) {
	d.mu.Lock()
	ss := d.sessions[phrase]
	delete(d.sessions, phrase)
	d.mu.Unlock()
	if ss != nil {
		ss.close()
	}
	d.changed()
}

// Close stops everything; the app is quitting.
func (d *Desk) Close() {
	d.mu.Lock()
	d.closed = true
	all, pairings, stop := d.sessions, d.pairings, d.bellStop
	d.sessions, d.pairings, d.bellStop = map[string]*sess{}, map[string]*pairing{}, nil
	d.mu.Unlock()
	if stop != nil {
		stop()
	}
	for _, ss := range all {
		ss.close()
	}
	for _, p := range pairings {
		p.s.Close()
	}
}

func (ss *sess) close() {
	if ss.timer != nil {
		ss.timer.Stop()
	}
	ss.s.Close()
	if ss.files != nil {
		_ = ss.files.Close()
	}
}

func (d *Desk) has(phrase string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.sessions[phrase]
	return ok
}

// adopt registers a session and runs its event loop.
func (d *Desk) adopt(ss *sess) {
	phrase := ss.s.Phrase()
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		ss.close()
		return
	}
	d.sessions[phrase] = ss
	ss.timer = time.AfterFunc(ShareLifetime, func() { d.Stop(phrase) })
	d.mu.Unlock()
	d.changed()
	go ss.s.Run(func(ev any) { d.onEvent(phrase, ss, ev) })
}

func (d *Desk) onEvent(phrase string, ss *sess, ev any) {
	var after func()
	d.mu.Lock()
	switch e := ev.(type) {
	case share.Peers:
		ss.peers = e.Count
		if ss.role == roleShare && ss.status == "waiting" && e.Count > 0 {
			ss.status = "connected"
		}
	case share.Reconnecting:
		ss.status = "reconnecting"
	case share.Resumed:
		ss.status = "connected"
	case share.Closed:
		ss.status = "closed"
		if d.sessions[phrase] == ss {
			delete(d.sessions, phrase)
		}
	case xfer.Delivered:
		ss.done++
		ss.status = "delivered"
	case xfer.Offered:
		after = d.offered(ss, e)
	case xfer.Progress:
		if o := ss.find(e.From, e.ID); o != nil {
			o.done = e.Done
		}
	case xfer.Received:
		after = d.received(ss, e)
	case xfer.Failed:
		after = d.failed(ss, e.From, e.ID, e.Err)
	case xfer.Withdrawn:
		if o := ss.find(e.From, e.ID); o != nil {
			o.status = "withdrawn"
		}
	}
	d.mu.Unlock()
	if after != nil {
		after()
	}
	d.changed()
}

func (ss *sess) find(from wire.ParticipantID, id xfer.OfferID) *offer {
	for _, o := range ss.offers {
		if o.from == from && o.id == id {
			return o
		}
	}
	return nil
}

// offered accepts an offer on a receive session. Called with mu held; the
// returned func runs after it is released.
func (d *Desk) offered(ss *sess, e xfer.Offered) func() {
	if ss.role != roleReceive || ss.find(e.From, e.ID) != nil {
		return nil
	}
	o := &offer{from: e.From, id: e.ID, sum: e.Summary, status: "receiving"}
	ss.offers = append(ss.offers, o)
	ss.title = e.Summary.Name
	if ss.ask {
		o.status = "offered"
		ss.status = "offered"
		n := Note{Kind: "offer", Title: ss.peer + " wants to send " + e.Summary.Name,
			Body: describe(e.Summary), Phrase: ss.s.Phrase(), From: uint32(e.From), OfferID: e.ID.String()}
		return func() { d.cfg.Notify(n) }
	}
	ss.status = "receiving"
	return d.accept(ss, o)
}

// accept starts pulling an offer into the right sink. Called with mu held;
// the returned func runs after it is released.
func (d *Desk) accept(ss *sess, o *offer) func() {
	o.status = "receiving"
	var snk sink.Sink
	if clipboardKind(o.sum) {
		o.mem = sink.NewMemory(memoryMax)
		snk = o.mem
	} else {
		dir, err := sink.NewDir(d.cfg.Downloads)
		if err != nil {
			o.status, o.err = "failed", err.Error()
			return nil
		}
		o.dir, snk = dir, dir
	}
	x := ss.s.X
	// Accept emits into the same mux stream this event came from, so it
	// must not run on the goroutine draining that stream.
	return func() {
		go func() {
			if err := x.Accept(o.from, o.id, snk, manifest.Default); err != nil {
				d.mu.Lock()
				o.status, o.err = "failed", err.Error()
				d.mu.Unlock()
				d.changed()
			}
		}()
	}
}

// clipboardKind: a single text or image small enough to hold in memory goes
// to the clipboard rather than to disk.
func clipboardKind(s xfer.Summary) bool {
	return s.Items == 1 && s.Bytes <= memoryMax && (s.Kind == manifest.KindText || s.Kind == manifest.KindImage)
}

func (d *Desk) received(ss *sess, e xfer.Received) func() {
	o := ss.find(e.From, e.ID)
	if o == nil {
		return nil
	}
	o.status, o.done = "received", o.sum.Bytes
	ss.status = "received"
	if o.dir != nil {
		o.placed = d.cfg.Downloads
		placed := strings.Join(o.dir.Placed(), ", ")
		folder := d.cfg.Downloads
		return func() {
			d.cfg.Notify(Note{Kind: "received", Title: "Received " + placed, Body: human(o.sum.Bytes) + " in " + folder, Folder: folder})
		}
	}
	data, _ := o.mem.Item(0)
	if o.sum.Kind == manifest.KindText {
		o.text = preview(string(data))
	}
	return func() { d.toClipboard(o, data) }
}

// toClipboard puts received text or an image on the clipboard; an image is
// also saved, since a clipboard is easy to lose.
func (d *Desk) toClipboard(o *offer, data []byte) {
	var err error
	body := "copied to the clipboard"
	if o.sum.Kind == manifest.KindText {
		err = d.cfg.Clipboard.WriteText(string(data))
	} else {
		err = d.cfg.Clipboard.WritePNG(data)
		if p, serr := d.saveImage(data); serr == nil {
			body += " · saved as " + filepath.Base(p)
		}
	}
	if err != nil {
		body = "couldn't reach the clipboard: " + err.Error()
	}
	d.cfg.Notify(Note{Kind: "received", Title: "Received " + o.sum.Kind.String(), Body: body})
}

func (d *Desk) saveImage(png []byte) (string, error) {
	dir, err := sink.NewDir(d.cfg.Downloads)
	if err != nil {
		return "", err
	}
	it := xfer.ItemFor(stamp("image", ".png"), manifest.KindImage, "image/png", png)
	if err := dir.Prepare(manifest.Manifest{Items: []manifest.Item{it}}); err != nil {
		return "", err
	}
	w, err := dir.Create(0)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(png); err != nil {
		_ = w.Abort()
		return "", err
	}
	if err := w.Commit(); err != nil {
		return "", err
	}
	_ = dir.Finish()
	return filepath.Join(d.cfg.Downloads, dir.Placed()[0]), nil
}

func (d *Desk) failed(ss *sess, from wire.ParticipantID, id xfer.OfferID, err error) func() {
	o := ss.find(from, id)
	if o == nil {
		return nil
	}
	o.status, o.err = "failed", err.Error()
	name := o.sum.Name
	return func() {
		d.cfg.Notify(Note{Kind: "failed", Title: "Couldn't receive " + name, Body: err.Error()})
	}
}

func (d *Desk) changed() { d.cfg.Changed(d.State()) }

func topLevel(m manifest.Manifest) []string {
	var tops []string
	for _, it := range m.Items {
		top, _, _ := strings.Cut(it.Path, "/")
		if !slices.Contains(tops, top) {
			tops = append(tops, top)
		}
	}
	return tops
}

func stamp(prefix, ext string) string {
	return prefix + "-" + time.Now().Format("2006-01-02-150405") + ext
}

func preview(s string) string {
	const max = 280
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func human(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1000 && i < len(units)-1 {
		f /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
