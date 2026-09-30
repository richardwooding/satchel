package proto

import (
	"encoding/hex"
	"testing"

	"github.com/richardwooding/parley/phrase"
)

// Pin the derivation under satchel's label. Session IDs are shared between
// independently built clients (tray, browser) and the hosted relay; changing
// this constant knowingly is a protocol version bump, not a refactor. The
// value was cross-checked outside Go:
//
//	printf 'satchel/v1/session-id\0lion-42-maple' | sha256sum | cut -c1-32
func TestSessionIDGolden(t *testing.T) {
	got := phrase.SessionID(Label, "lion-42-maple")
	if h := hex.EncodeToString(got[:]); h != "ad3dd62253c24a3ce4192e94af0de36e" {
		t.Fatalf("session-ID derivation changed: %s", h)
	}
}
