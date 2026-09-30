# CLAUDE.md

## What this is

satchel is croc-style sharing from the system tray: clipboard text and
images, files and folders, to a code phrase (`lion-42-maple`) or to a paired
device. Built on github.com/richardwooding/parley — the phrase seeds a PAKE
handshake and every byte travels end-to-end encrypted through a blind relay.
Three surfaces share one Go core: a Wails v3 tray app (`app/`), a browser
page (Go compiled to wasm), and the relay server that also serves the page.

Plan: `~/.claude/plans/staged-swimming-dawn.md`.

## Commands

```sh
go test -race ./...          # everything; -short skips the 64 MiB transfer
go fix -diff ./...           # CI fails on any diff
golangci-lint run
GOOS=js GOARCH=wasm go build ./internal/...   # the core must build for the browser
```

Run `go fix -diff` and `golangci-lint run` before every push.

## Architecture and invariants

- **The relay is blind.** It sees session IDs and opaque frames. Never add
  server logic that reads payloads.
- **Protocol label**: every session.Host/Join passes `proto.Options()`
  (label `satchel/v1`). internal/proto's golden test pins the session-ID
  derivation; the value was cross-checked with sha256sum. Changing the label
  is a protocol version bump.
- **The receiver drives every transfer** (internal/xfer). A sender
  broadcasts an offer, then only answers pulls; at most `Window` (16) chunks
  of `ChunkSize` (48 KiB) are ever in flight to anyone. Measured: 128 in
  flight survives a throttled receiver, 512 gets it dropped by the relay.
  Do not raise ChunkSize — an oversize frame desyncs a parley session.
- **Offers reach late joiners by hello, not snapshot.** A joiner broadcasts
  hello on Attach and every member replays its standing offers; senders need
  not be the host, and a parley snapshot is capped at one frame. Rebind
  re-Attaches, which re-pulls in-flight transfers at once.
- **Peers are hostile.** internal/manifest validates every path before the
  sink sees it (fuzzed: FuzzPath, FuzzDecode); internal/sink's Dir writes
  only through os.Root and never overwrites; every item's SHA-256 is checked
  before commit. The jail is the defence even if validation is wrong.
- Handlers in xfer decide under `mu` and act after releasing it (`effects`):
  never write to the network or call Emit with `mu` held.
- **Zero `syscall/js` outside `cmd/satchel-wasm`.** internal/* compiles
  natively and to wasm; internal/sink/dir.go is `!js`.

## Desktop app (Wails v3, `app/`)

Measured on GNOME Wayland (Bluefin) with Wails v3.0.0-beta.26:
- Left-click = `OnClick`, right-click = menu. Attached popovers cannot be
  positioned on Wayland, so the design is menu-first plus a normal window.
- File drop needs an element with `data-file-drop-target` AND
  `@wailsio/runtime` imported — the drop round-trips through JS, and
  without the runtime nothing fires and nothing errors.
- Rebuild the tray menu only while it is closed (wails#4719); dbusmenu item
  IDs change on rebuild, so find items by label in tests.
- Builds need gtk4-devel + webkitgtk6.0-devel: on Bluefin use the
  `satchel-dev` distrobox with `GOTOOLCHAIN=go1.27.1`.
- Clipboard images on Wayland go through `wl-copy`/`wl-paste`; Wails'
  clipboard is text only.

## Releasing

Tags release, not merges. Commits as richard.wooding@gmail.com.
