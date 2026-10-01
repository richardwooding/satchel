// Command satchel is the tray app: share the clipboard or files by code
// phrase, receive by phrase, and hand things to the browser page.
//
// Everything that is not drawing lives in internal/desk and is tested there;
// this package wires desk to Wails — tray, window, notifications, deep links.
package main

import (
	"context"
	"embed"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/richardwooding/satchel/internal/clip"
	"github.com/richardwooding/satchel/internal/desk"
	"github.com/richardwooding/satchel/internal/pair"
)

//go:embed all:frontend/dist
var assets embed.FS

// appID names satchel everywhere the desktop needs one name: the GTK
// application and Wayland app_id (which GNOME uses to match a window to its
// .desktop file), the single-instance bus name, the .desktop file, the icon,
// and the Flatpak. Flathub wants io.github.* for GitHub-hosted apps.
const appID = "io.github.richardwooding.satchel"

// version is stamped at release with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg := loadSettings()
	notes := notifications.New()
	svc := &Service{settings: cfg}

	app := application.New(application.Options{
		Name:        "satchel",
		Description: "Croc-style sharing from the system tray",
		Services: []application.Service{
			application.NewService(notes),
			application.NewService(svc),
		},
		Assets: application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Linux: application.LinuxOptions{
			DisableQuitOnLastWindowClosed: true,
			// The program name, and so the Wayland app_id, inherits this.
			ApplicationID: appID + profileSuffix("_"),
		},
		Mac: application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: appID + profileSuffix("."),
			OnSecondInstanceLaunch: func(d application.SecondInstanceData) {
				if !svc.handleArgs(d.Args) {
					svc.showWindow("")
				}
			},
		},
	})
	svc.app = app
	svc.notes = notes

	svc.window = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "satchel",
		Width:            460,
		Height:           640,
		MinWidth:         380,
		MinHeight:        480,
		Hidden:           true,
		EnableFileDrop:   true,
		BackgroundColour: application.NewRGB(13, 17, 23),
		URL:              "/",
	})
	// Closing the window hides it: satchel lives in the tray.
	svc.window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		svc.window.Hide()
		e.Cancel()
	})
	svc.window.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		svc.shareDropped(e.Context().DroppedFiles())
	})

	svc.clip = clip.New(wailsText{app})
	pairs, err := openConfigPairs()
	if err != nil {
		log.Printf("paired devices unavailable: %v", err)
	}
	svc.desk = desk.New(desk.Config{
		RelayURL:   cfg.Relay,
		Downloads:  cfg.Downloads,
		Clipboard:  svc.clip,
		Changed:    svc.changed,
		Notify:     svc.notify,
		Pairs:      pairs,
		DeviceName: cfg.DeviceName,
	})
	svc.desk.StartBell()
	svc.setupNotifications()
	svc.setupTray()

	// A cold start from a satchel:// link carries it as the only argument.
	app.Event.OnApplicationEvent(events.Common.ApplicationLaunchedWithUrl, func(e *application.ApplicationEvent) {
		svc.handleArgs([]string{e.Context().URL()})
	})

	err = app.Run()
	svc.desk.Close()
	if err != nil {
		log.Fatal(err)
	}
}

// handleArgs acts on a satchel://join/<phrase> link among args, reporting
// whether there was one.
func (s *Service) handleArgs(args []string) bool {
	for _, a := range args {
		phrase, ok := phraseFromLink(a)
		if !ok {
			continue
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := s.desk.Receive(ctx, phrase); err != nil {
				s.notify(desk.Note{Kind: "failed", Title: "Couldn't join " + phrase, Body: err.Error()})
				return
			}
			s.showWindow("receive")
		}()
		return true
	}
	return false
}

// phraseFromLink accepts satchel://join/<phrase> and, for convenience, a
// browser share link (https://host/#<phrase>).
func phraseFromLink(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	var phrase string
	switch u.Scheme {
	case "satchel":
		// satchel://join/<phrase> parses with Host "join".
		if u.Host != "join" {
			return "", false
		}
		phrase = strings.Trim(u.Path, "/")
	case "https", "http":
		phrase = u.Fragment
	default:
		return "", false
	}
	phrase = strings.ToLower(strings.TrimSpace(phrase))
	if phrase == "" || strings.ContainsAny(phrase, "/?#") || len(phrase) > 128 {
		return "", false
	}
	return phrase, true
}

// wailsText adapts the Wails clipboard to clip's text fallback.
type wailsText struct{ app *application.App }

func (w wailsText) Text() (string, bool)  { return w.app.Clipboard.Text() }
func (w wailsText) SetText(s string) bool { return w.app.Clipboard.SetText(s) }

func openConfigPairs() (*pair.Store, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return openPairs(dir)
}

// profileSuffix namespaces a second copy on one machine — for trying pairing
// with yourself: SATCHEL_PROFILE=b XDG_CONFIG_HOME=/tmp/b satchel. It keeps
// the instances from forwarding to each other and from sharing a keyring
// identity. Unset, it is empty and changes nothing.
func profileSuffix(sep string) string {
	if p := os.Getenv("SATCHEL_PROFILE"); p != "" {
		return sep + p
	}
	return ""
}
