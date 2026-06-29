# AlkenaCode POS

A lean, offline-first point of sale for small shops. It runs as a tiny local
agent on the shop's own computer, serves a simple till interface in the browser,
auto-detects the thermal receipt printer, records every sale offline, and prints
clean receipts. No internet required. It can optionally sync to the AlkenaCode
cloud for backup and multi-device access.

See `PRODUCT.md` for the product brief and `DESIGN.md` for the visual direction.

## Download

Windows builds are published on the
[**Releases**](https://github.com/Kiragu-Maina/pos-agent/releases) page. Download
`pos.exe`, run it, and the till opens in your browser. It works fully offline.

## Code signing

Windows release binaries are code-signed. Free code signing is provided by
[**SignPath.io**](https://signpath.io), with a certificate from the
[**SignPath Foundation**](https://signpath.org) open-source program.

## Why a local agent and not a pure web app

Browsers cannot open raw TCP sockets, so a page in a browser cannot scan the
network or talk ESC/POS to a printer on port 9100. The local agent does that
work natively and serves the UI to the browser over loopback. As a bonus, this
makes the whole app work with no internet.

## Layout

```
cmd/pos/            entry point: serves the UI on 127.0.0.1:7777, opens browser
internal/web/       HTTP server, JSON API, embedded UI (ui/)
internal/scan/      LAN sweep for printer ports (9100 / 515 / 631)
internal/printer/   ESC/POS receipts over the network
internal/store/     offline sales storage (bbolt)
internal/cloudsync/ optional background sync to the cloud
```

## Run (development)

```
go run ./cmd/pos
```

Then open http://127.0.0.1:7777/ (the agent also opens it for you).

## Build for Windows

Release binaries are 32-bit (they run on both 32- and 64-bit Windows) and MUST be
built with a **Go 1.20.x toolchain** — Go 1.21 dropped the Windows 7 runtime.
The code is pure-Go (no cgo), so it is a simple cross-compile:

```
make build-windows      # 32-bit, GUI subsystem, icon + version metadata
```

## License

[MIT](LICENSE) © AlkenaCode Creations.
