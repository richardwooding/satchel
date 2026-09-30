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
	st := State{Shares: []Session{}, Receives: []Session{}}
	for phrase, ss := range d.sessions {
		v := Session{
			Phrase: phrase, Link: d.Link(phrase), Title: ss.title, Items: ss.items, Bytes: ss.bytes,
			Peers: ss.peers, Done: ss.done, Status: ss.status, Started: ss.started,
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
	newest := func(a, b Session) int { return b.Started.Compare(a.Started) }
	slices.SortFunc(st.Shares, newest)
	slices.SortFunc(st.Receives, newest)
	return st
}
