GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.20-dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo local)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -s -w \
	-X github.com/tash/tash/internal/version.Version=$(VERSION) \
	-X github.com/tash/tash/internal/version.Commit=$(COMMIT) \
	-X github.com/tash/tash/internal/version.BuildDate=$(BUILD_DATE)

.PHONY: all build install test vet fmt run release installers clean

all: build

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/tash ./cmd/tash

install: build
	install -d $(HOME)/.local/bin
	install -m 0755 bin/tash $(HOME)/.local/bin/tash

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './dist/*')

run: build
	./bin/tash dashboard

release:
	./scripts/build.sh --clean

installers:
	./scripts/build.sh --clean

clean:
	rm -rf bin dist
