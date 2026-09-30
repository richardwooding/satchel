// Package bell is the wake-up call for paired devices: a sender rings a
// receiver's inbox so the receiver knows to join a rendezvous session.
//
// The server side is as blind as the relay it sits beside. An inbox is an
// opaque 16-byte ID that rotates daily, a ring is at most MaxRing opaque
// bytes, and nothing outlives Pending. It cannot tell who rang whom, what the
// ring means, or whether anything followed. It can see which IP addresses
// listen and ring — like any server can — which is why the IDs rotate: a
// leaked inbox ID stops working within a day.
//
// A receiver long-polls GET /bell?ids=<hex>[,<hex>] and gets back the rings
// waiting for any of those inboxes, or 204 after Wait. A sender POSTs the
// ring's bytes to /bell/<hex>. A ring nobody is listening for is held for
// Pending, so a receiver between polls still hears it.
package bell

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ID is an inbox.
type ID [16]byte

func (id ID) String() string { return hex.EncodeToString(id[:]) }

// ParseID reads a hex inbox ID.
func ParseID(s string) (ID, error) {
	var id ID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		return id, errors.New("bell: bad inbox id")
	}
	copy(id[:], b)
	return id, nil
}

const (
	// MaxRing bounds a ring's payload. satchel's is 24 bytes.
	MaxRing = 64
	// MaxIDs bounds how many inboxes one poll may listen on.
	MaxIDs = 4
	// perInbox bounds rings held for one inbox.
	perInbox = 8
)

// Ring is one delivered ring, as returned to a listener.
type Ring struct {
	ID   string `json:"id"`
	Data []byte `json:"data"`
}

// Options tune a Server; zero values take the defaults.
type Options struct {
	Wait      time.Duration // how long a poll waits for a ring (default 50s)
	Pending   time.Duration // how long an unheard ring is held (default 60s)
	RingRate  rate.Limit    // rings per second per IP (default 1)
	RingBurst int           // (default 10)
	MaxPolls  int           // concurrent polls per IP (default 16)
	// TrustFlyClientIP takes the client address from Fly's Fly-Client-IP
	// header. Set it only behind Fly's proxy, which overwrites the header;
	// anywhere else a client could forge it to escape the per-IP limits.
	TrustFlyClientIP bool
}

// Server is the bell's http.Handler: mount it at /bell and /bell/.
type Server struct {
	opt Options

	mu      sync.Mutex
	inboxes map[ID]*inbox
	rings   map[string]*rate.Limiter // by IP
	polls   map[string]int           // concurrent polls by IP
}

type inbox struct {
	held    []held
	waiters map[chan struct{}]struct{}
}

type held struct {
	data []byte
	at   time.Time
}

// New returns a bell server.
func New(opt Options) *Server {
	if opt.Wait == 0 {
		opt.Wait = 50 * time.Second
	}
	if opt.Pending == 0 {
		opt.Pending = 60 * time.Second
	}
	if opt.RingRate == 0 {
		opt.RingRate = 1
	}
	if opt.RingBurst == 0 {
		opt.RingBurst = 10
	}
	if opt.MaxPolls == 0 {
		opt.MaxPolls = 16
	}
	return &Server{opt: opt, inboxes: map[ID]*inbox{}, rings: map[string]*rate.Limiter{}, polls: map[string]int{}}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/bell/"):
		s.ring(w, r, strings.TrimPrefix(r.URL.Path, "/bell/"))
	case r.Method == http.MethodGet && r.URL.Path == "/bell":
		s.poll(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) ring(w http.ResponseWriter, r *http.Request, rawID string) {
	id, err := ParseID(rawID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.limiter(s.clientIP(r)).Allow() {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, MaxRing+1))
	if err != nil || len(data) == 0 || len(data) > MaxRing {
		http.Error(w, "bad ring", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	in := s.inboxFor(id)
	s.expire(in, time.Now())
	if len(in.held) >= perInbox {
		s.mu.Unlock()
		http.Error(w, "inbox full", http.StatusTooManyRequests)
		return
	}
	in.held = append(in.held, held{data: data, at: time.Now()})
	for ch := range in.waiters {
		close(ch)
		delete(in.waiters, ch)
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) poll(w http.ResponseWriter, r *http.Request) {
	ids, err := parseIDs(r.URL.Query().Get("ids"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ip := s.clientIP(r)
	if !s.enter(ip) {
		http.Error(w, "too many polls", http.StatusTooManyRequests)
		return
	}
	defer s.leave(ip)

	wake := make(chan struct{})
	if rings := s.take(ids, wake); len(rings) > 0 {
		writeRings(w, rings)
		return
	}
	defer s.unwait(ids, wake)
	select {
	case <-wake:
		writeRings(w, s.take(ids, nil))
	case <-time.After(s.opt.Wait):
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
	}
}

// take removes and returns rings held for any of ids. With wake non-nil and
// nothing held, it registers wake to be closed by the next ring.
func (s *Server) take(ids []ID, wake chan struct{}) []Ring {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []Ring
	for _, id := range ids {
		in := s.inboxFor(id)
		s.expire(in, now)
		for _, h := range in.held {
			out = append(out, Ring{ID: id.String(), Data: h.data})
		}
		in.held = nil
	}
	if len(out) == 0 && wake != nil {
		for _, id := range ids {
			s.inboxFor(id).waiters[wake] = struct{}{}
		}
	}
	for _, id := range ids {
		s.gc(id)
	}
	return out
}

func (s *Server) unwait(ids []ID, wake chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if in, ok := s.inboxes[id]; ok {
			delete(in.waiters, wake)
			s.gc(id)
		}
	}
}

func (s *Server) inboxFor(id ID) *inbox {
	in, ok := s.inboxes[id]
	if !ok {
		in = &inbox{waiters: map[chan struct{}]struct{}{}}
		s.inboxes[id] = in
	}
	return in
}

func (s *Server) expire(in *inbox, now time.Time) {
	kept := in.held[:0]
	for _, h := range in.held {
		if now.Sub(h.at) < s.opt.Pending {
			kept = append(kept, h)
		}
	}
	in.held = kept
}

// gc forgets an inbox with nothing held and nobody waiting: the server keeps
// no record of inboxes it is not actively serving.
func (s *Server) gc(id ID) {
	if in, ok := s.inboxes[id]; ok && len(in.held) == 0 && len(in.waiters) == 0 {
		delete(s.inboxes, id)
	}
}

func (s *Server) limiter(ip string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.rings[ip]
	if !ok {
		if len(s.rings) > 10_000 {
			s.rings = map[string]*rate.Limiter{} // crude bound; a flood resets it
		}
		l = rate.NewLimiter(s.opt.RingRate, s.opt.RingBurst)
		s.rings[ip] = l
	}
	return l
}

func (s *Server) enter(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.polls[ip] >= s.opt.MaxPolls {
		return false
	}
	s.polls[ip]++
	return true
}

func (s *Server) leave(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.polls[ip]--; s.polls[ip] <= 0 {
		delete(s.polls, ip)
	}
}

// Held reports how many inboxes the server currently holds state for.
func (s *Server) Held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inboxes)
}

func parseIDs(q string) ([]ID, error) {
	parts := strings.Split(q, ",")
	if q == "" || len(parts) > MaxIDs {
		return nil, errors.New("bell: need 1 to 4 inbox ids")
	}
	ids := make([]ID, 0, len(parts))
	for _, p := range parts {
		id, err := ParseID(p)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func writeRings(w http.ResponseWriter, rings []Ring) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(rings)
}

// clientIP is the socket address, or Fly's client header when trusted.
func (s *Server) clientIP(r *http.Request) string {
	if ip := r.Header.Get("Fly-Client-IP"); ip != "" && s.opt.TrustFlyClientIP {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
