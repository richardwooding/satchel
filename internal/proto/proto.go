// Package proto pins satchel's wire-protocol domain label and session
// options. Every parley entry point that derives keys or session IDs must
// receive this label — changing it is a protocol version bump: clients on
// different labels derive different session IDs and keys and cannot talk.
package proto

import "github.com/richardwooding/parley/session"

// Label is passed via Options to every session.Host / session.Join call.
const Label = "satchel/v1"

// Options is the bundle every session.Host / session.Join call must pass.
// satchel uses parley's default role policy: sender and receivers are equal
// members, and "host" is only parley's snapshot and migration anchor.
func Options() []session.Option {
	return []session.Option{session.WithProtocol(Label)}
}
