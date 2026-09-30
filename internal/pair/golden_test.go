package pair

import (
	"encoding/hex"
	"testing"
	"time"
)

// These pin the derivations: paired devices built from different versions
// must agree on every one, so a change here unpairs everyone. The expected
// values were computed independently, with a Python HKDF over the standard
// library's hmac, not by running this code.
func TestDerivationsGolden(t *testing.T) {
	seq := func(from, n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(from + i)
		}
		return b
	}
	secret, nonce, inbox := seq(0, 32), seq(100, 16), seq(200, 32)
	day := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)

	id := InboxID(inbox, day)
	for name, c := range map[string]struct{ got, want string }{
		"inbox":      {hex.EncodeToString(id[:]), "6ed4c99c7912c346fe748a1f88e0939c"},
		"rendezvous": {Rendezvous(secret, nonce), "791497c2a90042bb0470704c0506422b"},
		"hint":       {hex.EncodeToString(hint(secret, nonce)), "609e1523361b1c69"},
		"check code": {CheckCode(secret), "355 813 648"},
	} {
		if c.got != c.want {
			t.Errorf("%s derivation changed: %s, want %s", name, c.got, c.want)
		}
	}
}
