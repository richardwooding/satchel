# satchel

Croc-style sharing from your system tray. Copy something, pick **Share
clipboard**, and send the link it copies for you; the other side opens it in
a browser — or types the code phrase (`lion-42-maple`) into their satchel —
and it arrives, end-to-end encrypted, through a relay that cannot read it or
even see its name. Pair a device once and send to it by name after that.

- **Clipboard** text and images, **files** and whole **folders**.
- **Anyone can receive** at [satchel-send.fly.dev](https://satchel-send.fly.dev)
  with nothing installed, and send back from a phone.
- **Paired devices**: pair once with a phrase and a check code, then *Send
  clipboard to ▸ laptop*. The receiver is always asked first.
- **Call ▸** starts a [confab](https://github.com/richardwooding/confab) video
  call and can invite a paired device to it.
- Nothing lands without a name of its own: received files never overwrite,
  every item's SHA-256 is checked, and paths from the other side cannot leave
  the downloads folder.

## Install (Linux)

From the [latest release](https://github.com/richardwooding/satchel/releases/latest):

| | |
|---|---|
| Fedora 40+ | `sudo dnf install ./satchel-<version>-1.x86_64.rpm` |
| Ubuntu 24.04+ / Debian 13+ | `sudo apt install ./satchel_<version>_amd64.deb` |
| Bluefin, Fedora Atomic, anything with Flatpak | `flatpak install --user ./satchel-<version>.flatpak` |

The Flatpak brings its own GTK and WebKitGTK through the GNOME runtime (it
fetches the runtime from Flathub if you do not have it) and bundles
wl-clipboard. There is no AppImage: WebKitGTK looks for its helper processes
at a path fixed when it was built, so an AppImage made on one distro crashes on
another — v0.3.0's did, on Fedora.

The rpm and deb also pull in **wl-clipboard** (a recommendation, not a hard
dependency): on Wayland it is how satchel reads the clipboard while it sits in
the tray, and how images get onto it.

On GNOME the tray icon needs the
[AppIndicator extension](https://extensions.gnome.org/extension/615/appindicator-support/)
— Ubuntu and Bluefin ship it; on Fedora Workstation install
`gnome-shell-extension-appindicator`.

Left-click the bag in the tray for the window; right-click for the menu.

## How it works

Built on [parley](https://github.com/richardwooding/parley): the phrase seeds
a PAKE, everything travels XChaCha20-Poly1305 through a relay that only ever
sees session IDs and opaque frames, and the receiver pulls every chunk so a
slow device is never dropped. Paired devices find each other through a blind
wake-up endpoint that sees an inbox ID rotating daily and 24 opaque bytes.
`CLAUDE.md` has the design and its invariants.

## Building

```sh
go test -race ./...                 # needs gtk4 + webkitgtk-6.0 headers (cgo)
make serve                          # relay + browser page on :8080
cd app && wails3 task build         # the tray app (Wails v3)
cd app && wails3 task package VERSION=0.0.0   # deb and rpm in app/bin
flatpak-builder --user --install-deps-from=flathub --force-clean --repo=repo build \
  app/build/linux/flatpak/io.github.richardwooding.satchel.yml   # the Flatpak
```

MIT licensed.
