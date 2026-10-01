package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/richardwooding/satchel/internal/clip"
	"github.com/richardwooding/satchel/internal/desk"
)

// Service is what the window calls. Every method returns quickly or runs
// the slow part in the background; results arrive as "state" events.
type Service struct {
	app      *application.App
	window   *application.WebviewWindow
	notes    *notifications.NotificationService
	desk     *desk.Desk
	clip     *clip.Clipboard
	settings Settings

	mu       sync.Mutex
	tray     *tray
	lastMenu string // the tray menu's shape, so progress alone never rebuilds it
}

// Emitted event names.
const (
	evState = "state" // desk.State, on every change
	evView  = "view"  // "send" | "receive": the tray asked to show a tab
	evBusy  = "busy"  // a long step's description, "" when done
)

func init() {
	application.RegisterEvent[desk.State](evState)
	application.RegisterEvent[string](evView)
	application.RegisterEvent[string](evBusy)
}

// State is the current snapshot, for the window's first render.
func (s *Service) State() desk.State { return s.desk.State() }

// Version is the app version.
func (s *Service) Version() string { return version }

// ShareClipboard offers the clipboard and copies the link to it.
func (s *Service) ShareClipboard() error {
	return s.shareThen(func(ctx context.Context) (string, error) { return s.desk.ShareClipboard(ctx) })
}

// ShareText offers text typed into the window.
func (s *Service) ShareText(text string) error {
	return s.shareThen(func(ctx context.Context) (string, error) { return s.desk.ShareText(ctx, text) })
}

// PickFiles opens the file dialog and shares what was chosen.
func (s *Service) PickFiles(folders bool) error {
	d := s.app.Dialog.OpenFile().
		SetTitle("Share with satchel").
		CanChooseFiles(!folders).
		CanChooseDirectories(folders).
		AttachToWindow(s.window)
	paths, err := d.PromptForMultipleSelection()
	if err != nil || len(paths) == 0 {
		return err
	}
	s.shareDropped(paths)
	return nil
}

// shareDropped shares files dropped on the window or picked in a dialog.
// Hashing a large folder takes a while, so it runs in the background.
func (s *Service) shareDropped(paths []string) {
	if len(paths) == 0 {
		return
	}
	go func() {
		err := s.shareThen(func(ctx context.Context) (string, error) {
			last := time.Time{}
			return s.desk.ShareFiles(ctx, paths, func(done, total int64, current string) {
				if time.Since(last) > 150*time.Millisecond {
					last = time.Now()
					s.app.Event.Emit(evBusy, fmt.Sprintf("preparing %s · %d%%", current, pct(done, total)))
				}
			})
		})
		if err != nil {
			s.notify(desk.Note{Kind: "failed", Title: "Couldn't share", Body: err.Error()})
		}
	}()
}

func (s *Service) shareThen(start func(context.Context) (string, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer s.app.Event.Emit(evBusy, "")
	phrase, err := start(ctx)
	if err != nil {
		return err
	}
	// The link is what gets pasted into a chat, so it goes on the clipboard.
	// Whatever was being shared is already captured in the offer.
	s.Copy(s.desk.Link(phrase))
	s.showWindow("send")
	return nil
}

// Receive joins a phrase.
func (s *Service) Receive(phrase string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.desk.Receive(ctx, phrase)
}

// Open starts a phrase for someone else to send to.
func (s *Service) Open() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.desk.Open(ctx)
}

// Stop closes a share or receive.
func (s *Service) Stop(phrase string) { s.desk.Stop(phrase) }

// Copy puts text on the clipboard, through wl-copy where there is one: GTK's
// clipboard needs a focused window on Wayland, and a tray app has none.
func (s *Service) Copy(text string) {
	if err := s.clip.WriteText(text); err != nil {
		s.app.Clipboard.SetText(text)
	}
}

// QR renders a link as a PNG data URL.
func (s *Service) QR(link string) string {
	png, err := qrcode.Encode(link, qrcode.Medium, 256)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// ShowFolder opens the downloads folder in the file manager.
func (s *Service) ShowFolder() error { return s.app.Env.OpenFileManager(s.settings.Downloads, false) }

// Settings returns the current settings.
func (s *Service) Settings() Settings { return s.settings }

// PickDownloads chooses where received files go. It applies on restart,
// since receives already open keep writing where they started.
func (s *Service) PickDownloads() (string, error) {
	dir, err := s.app.Dialog.OpenFile().
		SetTitle("Save received files in…").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		AttachToWindow(s.window).
		PromptForSingleSelection()
	if err != nil || dir == "" {
		return s.settings.Downloads, err
	}
	s.settings.Downloads = dir
	return dir, saveSettings(s.settings)
}

func (s *Service) showWindow(view string) {
	if view != "" {
		s.app.Event.Emit(evView, view)
	}
	s.window.Show()
	s.window.Focus()
}

// changed is desk's Changed hook: refresh the window and, when the set of
// sessions changed, the tray menu.
func (s *Service) changed(st desk.State) {
	s.app.Event.Emit(evState, st)
	s.refreshTray(st)
}

func pct(done, total int64) int64 {
	if total == 0 {
		return 100
	}
	return done * 100 / total
}
