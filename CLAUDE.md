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
  natively and to wasm; internal/sink/dir.go is `!js`. internal/share is
  the session lifecycle (host/join, reconnect backoff sized to the relay's
  30s grace and 5/min per-IP limit, friendly errors) for every surface.
- **Browser bridge**: exactly two functions, `satchel_send(json, files?)`
  and `satchelOnEvent(json, bytes?)`. JSON for commands and events; the
  optional second argument carries File objects in and received bytes out,
  so file data is never base64'd through JSON. The page writes every
  peer-supplied string with textContent, never innerHTML.

## Relay and deploy

`cmd/satchel-relay` serves web/dist at / and the relay at /ws (confab's
server, 8 members per session). `make wasm` builds the page; `make serve`
runs it on :8080. Unlike confab, **every shared byte crosses the relay** —
bandwidth is the cost to watch. Tag push → goreleaser → ghcr image;
`fly deploy --image ghcr.io/richardwooding/satchel:X.Y.Z` (Fly app
`satchel-send`; "satchel" was taken). One machine until
/bell routes by affinity.

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
  clipboard is text only. **All clipboard reads and writes go through
  internal/clip**, not `app.Clipboard`: on Wayland only a focused window may
  use GTK's clipboard, a tray app usually has none, and the read blocked
  forever. The fallback now times out. wl-clipboard is a runtime dependency
  (it is not in the distrobox by default: `sudo dnf install wl-clipboard`).
- GNOME's AppIndicator extension **drops a tray item with no Menu
  property** (journal: "PropertyNotFound … _checkNeededProperties"), so the
  first menu is built before Run. The tray *label* becomes the item's Id and
  Title (default "Wails") and must be set before Run. `SetTooltip` is a
  no-op on Linux in beta.26 — the green dot on the icon is the only "open"
  signal. Extensions go INACTIVE while the screen is locked; Wails
  re-registers when the watcher returns (seen working).
- Test the tray headlessly over D-Bus: `dbusmenu.GetLayout` lists items,
  `dbusmenu.Event <id> clicked` clicks one, `StatusNotifierItem.Activate`
  is a left-click. Look IDs up by label each time.
- Run the app with `XDG_CONFIG_HOME=<tmp>` and a settings.json pointing
  downloads at a temp dir when testing, so nothing lands in ~/Downloads.

## Releasing

Tags release, not merges. Commits as richard.wooding@gmail.com. A `v*` tag
runs release.yml: goreleaser builds the relay (archives `satchel-relay_*`,
ghcr image), then the `desktop` job builds the tray app on Ubuntu 24.04 with
`wails3 task package VERSION=<tag>` and attaches `satchel_<v>_amd64.deb`,
`satchel-<v>-1.x86_64.rpm` and `satchel-<v>.flatpak` plus
`desktop-checksums.txt`. **No AppImage**: WebKitGTK spawns its helpers from
a path compiled into the library — the Ubuntu-built v0.3.0 AppImage crashed
on Fedora, and the local one only "worked" by using the host's helpers.
The Flatpak (app/build/linux/flatpak) builds inside the GNOME 51 SDK so it
links against the runtime's glibc, **offline** as Flathub requires: Go
modules come from `go-sources.json` (written by `go run
./app/build/linux/flatpak/gosources`, proxy.golang.org files laid out as a
`GOPROXY=file://` dir; go.sum still verifies) and npm packages from
`node-sources.json` (flatpak-node-generator from flatpak-builder-tools).
wails3 is not used there — the generated TypeScript bindings in
app/frontend/bindings are committed, and CI's app job fails if they drift.
**After any go.mod or package-lock.json change, regenerate both source
lists** (commands in the manifest header) or CI's flatpak job fails; this
includes Dependabot PRs. CI's `flatpak` job keeps every push's
bundle as an artifact. One app ID everywhere: io.github.richardwooding.satchel
(GTK ApplicationID → Wayland app_id → .desktop name, icon, single-instance
bus name). In a Flatpak, "Start at login" goes through the Background
portal (app/autostart.go): Wails' XDG autostart file would land in the
sandbox's private config dir. nfpm reads `${VERSION}` from the environment (the
Taskfile exports it), and the `.desktop` file is generated with
`Exec=satchel %u` + `MimeType=x-scheme-handler/satchel;` — without `%u` no
link ever reaches the app. Never run `bin/satchel --version`: there is no
such flag, it launches the GUI.
