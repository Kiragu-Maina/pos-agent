# POS System

A lean, offline-first point of sale for small Kenyan shops. It runs as a tiny
local agent that serves a web UI on the shop's own computer, auto-detects the
thermal receipt printer, and prints receipts. No internet required.

See `PRODUCT.md` for the product brief and `DESIGN.md` for the visual direction.

## Why a local agent and not a pure web app

Browsers cannot open raw TCP sockets, so a page in a browser cannot scan the
network or talk ESC/POS to a printer on port 9100. The local agent does that
work natively and serves the UI to the browser over loopback. As a bonus, this
makes the whole app work with no internet.

## Layout

```
cmd/pos/            entry point: serves UI on 127.0.0.1:7777, opens browser
internal/web/       HTTP server, JSON API, embedded UI (ui/)
internal/scan/      LAN sweep for printer ports (9100 / 515 / 631)
internal/printer/   ESC/POS over the network (USB to follow)
```

## Run (development)

```
go run ./cmd/pos
```

Then open http://127.0.0.1:7777/ (the agent also opens it for you).

## Build for Windows 7 (important)

Release binaries for Windows 7 MUST be built with a **Go 1.20.x toolchain**.
Go 1.21 dropped the Windows 7 runtime, so newer toolchains produce binaries
that will not start on Win7. Keep the code pure-Go (no cgo) so this stays a
simple cross-compile:

```
# with a Go 1.20.x toolchain on PATH
GOOS=windows GOARCH=amd64 go build -o dist/pos.exe ./cmd/pos
```

## Status

v1 in progress. Working today: printer discovery and a test print. Next: the
sell screen, cash payment, receipt rendering, and offline sales storage.
M-Pesa (Daraja) is planned for v2.
