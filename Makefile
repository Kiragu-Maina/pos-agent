# AlkenaCode POS — local agent build helpers.
#
# The agent is pinned to Go 1.20, the last toolchain that produces Windows 7
# compatible binaries (Go 1.21+ dropped the Win7 runtime). Keep all code pure-Go
# (no cgo) so cross-compilation stays a simple one-liner.

GOBIN       := $(shell go env GOPATH)/bin
ICON        := build/windows/pos.ico
VERSIONINFO := build/windows/versioninfo.json
SYSO        := cmd/pos/resource_windows_386.syso
WIN_VERSION ?= 1.0.0

.PHONY: build test build-windows installer clean

build: ## Build the agent for the host OS.
	go build -o pos ./cmd/pos

test: ## Run the test suite.
	go test ./...

$(SYSO): $(VERSIONINFO) $(ICON)
	$(GOBIN)/goversioninfo -64=false -icon $(ICON) -o $(SYSO) $(VERSIONINFO)

build-windows: $(SYSO) ## Build the Win7 (32-bit) GUI agent: dist/pos.exe.
	GOTOOLCHAIN=go1.20.14 GOOS=windows GOARCH=386 CGO_ENABLED=0 \
	  go build -trimpath -ldflags="-s -w -H windowsgui" -o dist/pos.exe ./cmd/pos

installer: build-windows ## Build the Windows installer (needs makensis/NSIS).
	makensis -DVERSION=$(WIN_VERSION) build/windows/pos.nsi

clean:
	rm -f pos pos.exe $(SYSO)
	rm -rf dist
