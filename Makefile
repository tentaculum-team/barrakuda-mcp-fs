BINARY := barrakuda-mcp-fs
PKG    := ./cmd/api

GOFLAGS := -buildvcs=false

# Local dev binary name gets a .exe on Windows hosts.
EXE :=
ifeq ($(OS),Windows_NT)
	EXE := .exe
endif

.PHONY: build build-cross release test lint clean

# build: local development binary for the current host.
build:
	go build $(GOFLAGS) -o bin/$(BINARY)$(EXE) $(PKG)

# build-cross: one binary per OS this mod distributes for (Rust target-triple
# naming kept for consistency with the rest of this ecosystem, even though
# these are no longer bundled Tauri sidecars — see manifest.json's
# per-platform `package` in the app's catalog).
build-cross:
	GOOS=darwin  GOARCH=arm64 go build $(GOFLAGS) -o bin/$(BINARY)-aarch64-apple-darwin      $(PKG)
	GOOS=darwin  GOARCH=amd64 go build $(GOFLAGS) -o bin/$(BINARY)-x86_64-apple-darwin       $(PKG)
	GOOS=linux   GOARCH=amd64 go build $(GOFLAGS) -o bin/$(BINARY)-x86_64-unknown-linux-gnu  $(PKG)
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -o bin/$(BINARY)-x86_64-pc-windows-msvc.exe $(PKG)

# release: the zips a GitHub Release actually publishes — one per OS,
# entry binary (renamed to the bare name from manifest.json) + manifest.json.
# Upload with: gh release create v<version> release/*.zip
release: build-cross
	python make_release_zip.py $(BINARY)

test:
	go test $(GOFLAGS) ./...

# lint: no dedicated linter (golangci-lint etc.) is configured across the Go
# repos in this ecosystem, so `go vet` is the lint gate here.
lint:
	go vet $(GOFLAGS) ./...

clean:
	rm -rf bin release
