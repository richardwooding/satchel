package main

import (
	"context"
	"fmt"
	"time"

	"github.com/richardwooding/satchel/internal/desk"
)

// PairStart opens a pairing session and returns its phrase.
func (s *Service) PairStart() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.desk.PairStart(ctx)
}

// PairJoin joins another device's pairing session.
func (s *Service) PairJoin(phrase string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.desk.PairJoin(ctx, phrase)
}

// PairConfirm saves a device once the codes match.
func (s *Service) PairConfirm(phrase, peerID string) error { return s.desk.PairConfirm(phrase, peerID) }

// PairCancel closes a pairing session.
func (s *Service) PairCancel(phrase string) { s.desk.PairCancel(phrase) }

// Unpair forgets a device.
func (s *Service) Unpair(peerID string) error { return s.desk.Unpair(peerID) }

// ShareClipboardTo sends the clipboard to a paired device.
func (s *Service) ShareClipboardTo(peerID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.desk.ShareClipboardTo(ctx, peerID); err != nil {
		return err
	}
	s.showWindow("send")
	return nil
}

// PickFilesTo sends chosen files or a folder to a paired device.
func (s *Service) PickFilesTo(peerID string, folders bool) error {
	paths, err := s.app.Dialog.OpenFile().
		SetTitle("Send with satchel").
		CanChooseFiles(!folders).
		CanChooseDirectories(folders).
		AttachToWindow(s.window).
		PromptForMultipleSelection()
	if err != nil || len(paths) == 0 {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		defer s.app.Event.Emit(evBusy, "")
		err := s.desk.ShareFilesTo(ctx, peerID, paths, func(done, total int64, current string) {
			s.app.Event.Emit(evBusy, fmt.Sprintf("preparing %s · %d%%", current, pct(done, total)))
		})
		if err != nil {
			s.notify(desk.Note{Kind: "failed", Title: "Couldn't send", Body: err.Error()})
			return
		}
		s.showWindow("send")
	}()
	return nil
}

// Accept takes an offer from a paired device.
func (s *Service) Accept(phrase string, from uint32, offerID string) error {
	return s.desk.Accept(phrase, from, offerID)
}

// Decline refuses one.
func (s *Service) Decline(phrase string, from uint32, offerID string) {
	s.desk.Decline(phrase, from, offerID)
}

// SetDeviceName changes what paired devices see this one as. It applies to
// pairings made after the next restart.
func (s *Service) SetDeviceName(name string) error {
	s.settings.DeviceName = name
	return saveSettings(s.settings)
}

// Call starts a confab call in the browser and copies its join link.
func (s *Service) Call() error {
	host, join, err := s.desk.Call()
	if err != nil {
		return err
	}
	s.Copy(join)
	if err := s.app.Browser.OpenURL(host); err != nil {
		return err
	}
	s.notify(desk.Note{Kind: "share-ready", Title: "Call started", Body: "join link copied — paste it to whoever you're calling"})
	return nil
}

// CallPeer starts a call and invites a paired device to it.
func (s *Service) CallPeer(peerID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host, err := s.desk.CallPeer(ctx, peerID)
	if err != nil {
		return err
	}
	return s.app.Browser.OpenURL(host)
}
