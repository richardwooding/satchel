package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
)

// inFlatpak reports whether satchel runs inside a Flatpak sandbox.
func inFlatpak() bool { return os.Getenv("FLATPAK_ID") != "" }

// Autostart reports whether satchel starts at login. In a Flatpak the
// portal cannot be asked, so the last answer it gave is remembered.
func (s *Service) Autostart() bool {
	if inFlatpak() {
		return s.settings.Autostart
	}
	on, err := s.app.Autostart.IsEnabled()
	return err == nil && on
}

// SetAutostart turns start-at-login on or off. Inside a Flatpak, Wails'
// autostart would write an XDG autostart file into the sandbox's private
// config directory, which the desktop never reads — so it asks the
// Background portal instead, the only way a sandboxed app may.
func (s *Service) SetAutostart(on bool) error {
	if !inFlatpak() {
		if on {
			return s.app.Autostart.Enable()
		}
		return s.app.Autostart.Disable()
	}
	granted, err := requestBackground(on)
	if err != nil {
		return err
	}
	if on && !granted {
		return errors.New("start at login was not allowed — check Settings › Apps › satchel")
	}
	s.settings.Autostart = on
	return saveSettings(s.settings)
}

// requestBackground calls org.freedesktop.portal.Background.RequestBackground
// and waits for the portal's Response signal on the returned request.
func requestBackground(autostart bool) (bool, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false, err
	}
	token := fmt.Sprintf("satchel%d", time.Now().UnixNano())
	sender := conn.Names()[0][1:] // ":1.42" → "1.42"
	for i := range sender {
		if sender[i] == '.' {
			sender = sender[:i] + "_" + sender[i+1:]
		}
	}
	want := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
		dbus.WithMatchObjectPath(want),
	); err != nil {
		return false, err
	}
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)

	opts := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"reason":       dbus.MakeVariant("Keep satchel in the tray so paired devices can reach you"),
		"autostart":    dbus.MakeVariant(autostart),
		"commandline":  dbus.MakeVariant([]string{"satchel"}),
	}
	portal := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	var handle dbus.ObjectPath
	if err := portal.Call("org.freedesktop.portal.Background.RequestBackground", 0, "", opts).Store(&handle); err != nil {
		return false, fmt.Errorf("background portal: %w", err)
	}
	timeout := time.After(2 * time.Minute) // the portal may be showing a dialog
	for {
		select {
		case sig := <-signals:
			if sig.Path != handle && sig.Path != want {
				continue
			}
			if len(sig.Body) < 2 {
				return false, errors.New("background portal: malformed response")
			}
			code, _ := sig.Body[0].(uint32)
			results, _ := sig.Body[1].(map[string]dbus.Variant)
			if code != 0 {
				return false, nil
			}
			if !autostart {
				return true, nil // turning it off needs no permission
			}
			v, ok := results["autostart"]
			on, _ := v.Value().(bool)
			return ok && on, nil
		case <-timeout:
			return false, errors.New("background portal did not answer")
		}
	}
}
