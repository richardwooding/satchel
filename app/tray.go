package main

import (
	"fmt"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/richardwooding/satchel/app/icon"
	"github.com/richardwooding/satchel/internal/desk"
)

type tray struct {
	t            *application.SystemTray
	idle, active []byte
}

// setupTray builds the tray icon. Left-click opens the window; the menu is
// on right-click, which is how GNOME's AppIndicator delivers it.
func (s *Service) setupTray() {
	t := &tray{t: s.app.SystemTray.New(), idle: icon.Tray(false), active: icon.Tray(true)}
	// The label becomes the StatusNotifierItem's Id and Title, and Id cannot
	// change once the item is exported, so it is set before Run. (Wails'
	// default is "Wails".)
	t.t.SetLabel("satchel")
	t.t.SetIcon(t.idle)
	t.t.SetTooltip("satchel") // a no-op on Linux in v3.0.0-beta.26
	t.t.OnClick(func() { s.showWindow("") })
	// GNOME's AppIndicator extension drops an item with no Menu property, so
	// the first menu is built here unconditionally, before Run.
	t.t.SetMenu(s.buildMenu(desk.State{}))
	s.mu.Lock()
	s.tray = t
	s.lastMenu = menuShape(desk.State{})
	s.mu.Unlock()
}

// refreshTray updates icon and tooltip every time, but rebuilds the menu
// only when the set of sessions changed: rebuilding a menu while it is open
// breaks its click handlers (wails#4719), and progress updates are frequent.
func (s *Service) refreshTray(st desk.State) {
	s.mu.Lock()
	t := s.tray
	shape := menuShape(st)
	rebuild := t != nil && shape != s.lastMenu
	if rebuild {
		s.lastMenu = shape
	}
	s.mu.Unlock()
	if t == nil {
		return
	}
	n := len(st.Shares) + len(st.Receives)
	if n > 0 {
		t.t.SetIcon(t.active)
		t.t.SetTooltip(fmt.Sprintf("satchel · %d open", n))
	} else {
		t.t.SetIcon(t.idle)
		t.t.SetTooltip("satchel")
	}
	if rebuild {
		t.t.SetMenu(s.buildMenu(st))
	}
}

func menuShape(st desk.State) string {
	var b strings.Builder
	for _, p := range st.Peers {
		fmt.Fprintf(&b, "peer:%s|%s;", p.ID, p.Name)
	}
	for _, x := range append(st.Shares, st.Receives...) {
		fmt.Fprintf(&b, "%s|%s|%s;", x.Phrase, x.Title, x.Status)
	}
	return b.String()
}

func (s *Service) buildMenu(st desk.State) *application.Menu {
	m := s.app.NewMenu()
	m.Add("Share clipboard").OnClick(func(*application.Context) { go s.reportErr("share the clipboard", s.ShareClipboard) })
	m.Add("Share files…").OnClick(func(*application.Context) { go s.reportErr("share files", func() error { return s.PickFiles(false) }) })
	m.Add("Share a folder…").OnClick(func(*application.Context) {
		go s.reportErr("share a folder", func() error { return s.PickFiles(true) })
	})
	if len(st.Peers) > 0 {
		clip := m.AddSubmenu("Send clipboard to")
		files := m.AddSubmenu("Send files to")
		for _, p := range st.Peers {
			id := p.ID
			clip.Add(p.Name).OnClick(func(*application.Context) {
				go s.reportErr("send the clipboard", func() error { return s.ShareClipboardTo(id) })
			})
			files.Add(p.Name).OnClick(func(*application.Context) {
				go s.reportErr("send files", func() error { return s.PickFilesTo(id, false) })
			})
		}
	}
	call := m.AddSubmenu("Call")
	call.Add("New call").OnClick(func(*application.Context) { go s.reportErr("start a call", s.Call) })
	for _, p := range st.Peers {
		id := p.ID
		call.Add(p.Name).OnClick(func(*application.Context) {
			go s.reportErr("call "+p.Name, func() error { return s.CallPeer(id) })
		})
	}
	m.AddSeparator()
	m.Add("Receive with a phrase…").OnClick(func(*application.Context) { s.showWindow("receive") })
	m.Add("Receive from a phone…").OnClick(func(*application.Context) {
		go s.reportErr("open a phrase", func() error {
			if _, err := s.Open(); err != nil {
				return err
			}
			s.showWindow("receive")
			return nil
		})
	})

	if len(st.Shares)+len(st.Receives) > 0 {
		m.AddSeparator()
		for _, x := range st.Shares {
			s.sessionItem(m, "↑", x)
		}
		for _, x := range st.Receives {
			s.sessionItem(m, "↓", x)
		}
	}

	m.AddSeparator()
	m.Add("Pair a device…").OnClick(func(*application.Context) { s.showWindow("devices") })
	m.Add("Open satchel").OnClick(func(*application.Context) { s.showWindow("") })
	m.AddCheckbox("Start at login", s.Autostart()).OnClick(func(c *application.Context) {
		if err := s.SetAutostart(c.ClickedMenuItem().Checked()); err != nil {
			s.notify(desk.Note{Kind: "failed", Title: "Couldn't change start at login", Body: err.Error()})
		}
	})
	m.Add("Quit").OnClick(func(*application.Context) { s.app.Quit() })
	return m
}

// sessionItem adds one open session as a submenu: copy its link, or stop it.
func (s *Service) sessionItem(m *application.Menu, arrow string, x desk.Session) {
	phrase, link := x.Phrase, x.Link
	// A paired session's phrase is a 128-bit rendezvous nobody needs to see
	// or share: name the device instead, and offer no phrase or link.
	if x.Peer != "" {
		dir := map[string]string{"↑": "to", "↓": "from"}[arrow]
		sub := m.AddSubmenu(fmt.Sprintf("%s %s %s — %s", arrow, dir, x.Peer, statusLabel(x)))
		sub.Add("Stop").OnClick(func(*application.Context) { go s.Stop(phrase) })
		return
	}
	sub := m.AddSubmenu(fmt.Sprintf("%s %s — %s", arrow, phrase, statusLabel(x)))
	sub.Add("Copy link").OnClick(func(*application.Context) { s.Copy(link) })
	sub.Add("Copy phrase").OnClick(func(*application.Context) { s.Copy(phrase) })
	sub.Add("Stop").OnClick(func(*application.Context) { go s.Stop(phrase) })
}

func statusLabel(x desk.Session) string {
	switch {
	case x.Done > 0:
		return x.Title + " · delivered"
	case x.Status == "waiting":
		return x.Title + " · waiting"
	case x.Status == "offered":
		return x.Title + " · waiting for you"
	}
	return x.Title
}

// reportErr runs a tray action and turns a failure into a notification: a
// tray click has nowhere else to show one.
func (s *Service) reportErr(what string, f func() error) {
	if err := f(); err != nil {
		s.notify(desk.Note{Kind: "failed", Title: "Couldn't " + what, Body: err.Error()})
	}
}
