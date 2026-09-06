VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/dopeCape/better-nm/internal/version.Version=$(VERSION)
BIN := bin

.PHONY: all build daemon cli desktop test test-race test-integration lint fmt clean run-daemon

all: build

build: cli daemon

cli:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/bnm ./cmd/bnm

daemon:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/bnmd ./cmd/bnmd

desktop:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/bnm-desktop ./cmd/bnm-desktop

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
