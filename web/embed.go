// Package web embeds the built browser page (web/dist, produced by
// `make wasm`) into the relay binary.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
