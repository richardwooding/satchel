//go:build !js

package desk

import (
	"slices"
	"time"
)

// State is everything the window and tray render, as plain data: it is
// serialized straight to the frontend.
type State struct {
	Shares   []Session `json:"shares"`
	Receives []Session `json:"receives"`
	Peers    []Peer    `json:"peers"`
	Pairings []Pairing `json:"pairings"`
}

// Peer is a paired device.
type Peer struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Paired time.Time `json:"paired"`
}

// Pairing is an open pairing session and who has introduced themselves.
type Pairing struct {
	Phrase     string      `json:"phrase"`
	Link       string      `json:"link"`
	Candidates []Candidate `json:"candidates"`
}

// Candidate is a device waiting to be confirmed by its check code.
type Candidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// Session is one open share or receive.
type Session struct {
	Phrase  string    `json:"phrase"`
	Link    string    `json:"link"`
	Title   string    `json:"title"`
	Items   int       `json:"items"`
	Bytes   int64     `json:"bytes"`
	Peers   int       `json:"peers"`
	Done    int       `json:"delivered"`
	Status  string    `json:"status"`
	Started time.Time `json:"started"`
	Peer    string    `json:"peer,omitempty"` // the paired device, if any
	Offers  []Offer   `json:"offers,omitempty"`
}

// Offer is one incoming offer on a receive session.
type Offer struct {
	From   uint32 `json:"from"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Items  int    `json:"items"`
	Bytes  int64  `json:"bytes"`
	Done   int64  `json:"done"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Folder string `json:"folder,omitempty"`
	Text   string `json:"text,omitempty"`
}

// State snapshots every open session, newest first.
func (d *Desk) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Empty, not nil: a nil slice reaches the frontend as null.
	st := State{Shares: []Session{}, Receives: []Session{}, Peers: []Peer{}, Pairings: []Pairing{}}
	for phrase, ss := range d.sessions {
		v := Session{
			Phrase: phrase, Link: d.Link(phrase), Title: ss.title, Items: ss.items, Bytes: ss.bytes,
			Peers: ss.peers, Done: ss.done, Status: ss.status, Started: ss.started, Peer: ss.peer,
		}
		for _, o := range ss.offers {
			v.Offers = append(v.Offers, Offer{
				From: uint32(o.from), ID: o.id.String(), Name: o.sum.Name, Kind: o.sum.Kind.String(),
				Items: o.sum.Items, Bytes: o.sum.Bytes, Done: o.done, Status: o.status,
				Error: o.err, Folder: o.placed, Text: o.text,
			})
		}
		if ss.role == roleShare {
			st.Shares = append(st.Shares, v)
		} else {
			st.Receives = append(st.Receives, v)
		}
	}
	for phrase, p := range d.pairings {
		v := Pairing{Phrase: phrase, Link: d.Link(phrase), Candidates: []Candidate{}}
		for _, c := range p.candidates {
			v.Candidates = append(v.Candidates, Candidate{ID: c.Peer.ID, Name: c.Peer.Name, Code: c.Code})
		}
		st.Pairings = append(st.Pairings, v)
	}
	if d.cfg.Pairs != nil {
		for _, p := range d.cfg.Pairs.Peers() {
			st.Peers = append(st.Peers, Peer{ID: p.ID, Name: p.Name, Paired: p.Paired})
		}
	}
	newest := func(a, b Session) int { return b.Started.Compare(a.Started) }
	slices.SortFunc(st.Shares, newest)
	slices.SortFunc(st.Receives, newest)
	return st
}
