package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/richardwooding/satchel/internal/desk"
)

// Settings persist in the user config dir as settings.json.
type Settings struct {
	Relay     string `json:"relay"`
	Downloads string `json:"downloads"`
	// DeviceName is what paired devices see this one as.
	DeviceName string `json:"deviceName"`
}

func settingsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "satchel", "settings.json"), nil
}

func loadSettings() Settings {
	s := Settings{Relay: desk.DefaultRelay, Downloads: defaultDownloads(), DeviceName: defaultDeviceName()}
	p, err := settingsPath()
	if err != nil {
		return s
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return s
	}
	if err != nil {
		log.Printf("settings: %v", err)
		return s
	}
	var saved Settings
	if err := json.Unmarshal(b, &saved); err != nil {
		log.Printf("settings: %s is not valid JSON, using defaults: %v", p, err)
		return s
	}
	if saved.Relay != "" {
		s.Relay = saved.Relay
	}
	if saved.Downloads != "" {
		s.Downloads = saved.Downloads
	}
	if saved.DeviceName != "" {
		s.DeviceName = saved.DeviceName
	}
	return s
}

func saveSettings(s Settings) error {
	p, err := settingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

// defaultDownloads is the desktop's own downloads folder (xdg-user-dir
// knows when it has been renamed or localized) plus "satchel".
func defaultDownloads() string {
	if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil {
		if d := strings.TrimSpace(string(out)); d != "" {
			return filepath.Join(d, "satchel")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "satchel"
	}
	return filepath.Join(home, "Downloads", "satchel")
}

func defaultDeviceName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return strings.TrimSuffix(h, ".localdomain")
	}
	return "desktop"
}
