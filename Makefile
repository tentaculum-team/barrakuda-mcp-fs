BINARY := barrakuda-mcp-fs
PKG    := ./cmd/api

# This tree is intentionally NOT a git repo yet (plain files for now), so Go's
# VCS stamping has nothing to read and `go build` errors out. Disable it here.
# Once this becomes a real repo you can drop -buildvcs=false to get VCS stamps.
GOFLAGS := -buildvcs=false

# Local dev binary name gets a .exe on Windows hosts.
EXE :=
ifeq ($(OS),Windows_NT)
	EXE := .exe
endif

.PHONY: build build-cross test lint clean

# build: local development binary for the current host.
build:
	go build $(GOFLAGS) -o bin/$(BINARY)$(EXE) $(PKG)

# build-cross: the four targets the Tauri sidecar convention consumes (a future
# task wires these in as external binaries). Output names are
# <name>-<rust-target-triple>[.exe] — verified against TAURI_ENV_TARGET_TRIPLE
# in barrakuda-software (e.g. x86_64-pc-windows-msvc). GOOS/GOARCH are mapped to
# the matching Rust triples below.
build-cross:
	GOOS=darwin  GOARCH=arm64 go build $(GOFLAGS) -o bin/$(BINARY)-aarch64-apple-darwin      $(PKG)
	GOOS=darwin  GOARCH=amd64 go build $(GOFLAGS) -o bin/$(BINARY)-x86_64-apple-darwin       $(PKG)
	GOOS=linux   GOARCH=amd64 go build $(GOFLAGS) -o bin/$(BINARY)-x86_64-unknown-linux-gnu  $(PKG)
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -o bin/$(BINARY)-x86_64-pc-windows-msvc.exe $(PKG)

test:
	go test $(GOFLAGS) ./...

# lint: no dedicated linter (golangci-lint etc.) is configured across the Go
# repos in this ecosystem, so `go vet` is the lint gate here.
lint:
	go vet $(GOFLAGS) ./...

clean:
	rm -rf bin
