package main

import (
	"testing"

	"github.com/richardwooding/satchel/internal/desk"
)

func TestPhraseFromLink(t *testing.T) {
	cases := map[string]string{
		"satchel://join/lion-42-maple":               "lion-42-maple",
		"satchel://join/Lion-42-Maple/":              "lion-42-maple",
		"https://satchel-send.fly.dev/#otter-7-cove": "otter-7-cove",
		"satchel://send/lion-42-maple":               "",
		"satchel://join/":                            "",
		"satchel://join/a/b":                         "",
		"ftp://x/#lion-42-maple":                     "",
		"./bin/satchel":                              "",
		"--flag":                                     "",
	}
	for in, want := range cases {
		got, ok := phraseFromLink(in)
		if got != want || ok != (want != "") {
			t.Errorf("phraseFromLink(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// The first menu must be built even though the empty state's shape is ""
// — GNOME drops a tray item that has no menu. menuShape of the empty state
// is the value that bug compared against.
func TestMenuShapeDistinguishesSessions(t *testing.T) {
	if menuShape(desk.State{}) != "" {
		t.Fatal("empty state should have an empty shape")
	}
	a := desk.State{Shares: []desk.Session{{Phrase: "a-1-b", Title: "x", Status: "waiting"}}}
	b := desk.State{Shares: []desk.Session{{Phrase: "a-1-b", Title: "x", Status: "delivered"}}}
	if menuShape(a) == menuShape(b) {
		t.Fatal("a status change must rebuild the menu")
	}
	a.Shares[0].Bytes, b = 99, a
	if menuShape(a) != menuShape(b) {
		t.Fatal("progress alone must not rebuild the menu")
	}
}

// In a Flatpak the GTK app ID must equal FLATPAK_ID exactly, whatever the
// test profile says, or WebKit's sandbox aborts the app.
func TestGTKAppIDInFlatpak(t *testing.T) {
	t.Setenv("SATCHEL_PROFILE", "b")
	t.Setenv("FLATPAK_ID", "")
	if got := gtkAppID(); got != appID+"_b" {
		t.Fatalf("outside a flatpak: %q", got)
	}
	t.Setenv("FLATPAK_ID", appID)
	if got := gtkAppID(); got != appID {
		t.Fatalf("inside a flatpak: %q, want %q", got, appID)
	}
}
