VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/dopeCape/better-nm/internal/version.Version=$(VERSION)
BIN := bin

PREFIX ?= $(HOME)/.local

.PHONY: all build daemon cli desktop test test-race test-integration lint fmt clean run-daemon install uninstall

all: build

build: cli daemon

cli:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/bnm ./cmd/bnm

daemon:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/bnmd ./cmd/bnmd

# Fyne needs cgo and the GL/X11/Wayland headers; on NixOS run this inside `nix develop`.
desktop:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/bnm-desktop ./cmd/bnm-desktop

# Installs bnm, bnmd and bnm-desktop under $(PREFIX)/bin (default ~/.local/bin, which is
# on PATH on most desktops) plus the launcher entry and icon. `make desktop` first if you
# want the desktop app included; it is skipped when bin/bnm-desktop is absent.
install: build
	install -Dm755 $(BIN)/bnm $(PREFIX)/bin/bnm
	install -Dm755 $(BIN)/bnmd $(PREFIX)/bin/bnmd
	@if [ -x $(BIN)/bnm-desktop ]; then \
	  install -Dm755 $(BIN)/bnm-desktop $(PREFIX)/bin/bnm-desktop; \
	  install -Dm644 packaging/desktop/bnm-desktop.desktop $(PREFIX)/share/applications/bnm-desktop.desktop; \
	  sed -i 's|^Exec=bnm-desktop|Exec=$(PREFIX)/bin/bnm-desktop|' $(PREFIX)/share/applications/bnm-desktop.desktop; \
	  install -Dm644 packaging/desktop/bnm.png $(PREFIX)/share/icons/hicolor/256x256/apps/bnm.png; \
	  command -v update-desktop-database >/dev/null && update-desktop-database $(PREFIX)/share/applications || true; \
	  echo "installed bnm-desktop + launcher entry"; \
	else echo "bin/bnm-desktop not built; skipped (make desktop)"; fi
	@echo "installed bnm and bnmd to $(PREFIX)/bin"

uninstall:
	rm -f $(PREFIX)/bin/bnm $(PREFIX)/bin/bnmd $(PREFIX)/bin/bnm-desktop \
	  $(PREFIX)/share/applications/bnm-desktop.desktop $(PREFIX)/share/icons/hicolor/256x256/apps/bnm.png

test:
	go test ./...

test-race:
	go test -race ./...

# Needs python-dbusmock (nix develop provides it).
test-integration:
	BNM_INTEGRATION=1 go test -tags integration -count=1 ./internal/nm/...

lint:
	go vet ./...
	gofmt -l . | tee /dev/stderr | test -z "$$(cat)"

fmt:
	gofmt -w .

clean:
	rm -rf $(BIN)

run-daemon: daemon
	$(BIN)/bnmd --log-level debug
