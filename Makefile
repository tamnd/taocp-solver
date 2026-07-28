BIN := taocp
PKG := ./cmd/taocp
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

export CGO_ENABLED := 0

# A run host is not required to have a Go toolchain, so the deployable artifact
# is a static Linux binary built here.
DIST_OS := linux
DIST_ARCH := amd64
DIST_BIN := dist/$(BIN)-$(DIST_OS)-$(DIST_ARCH)
HOST ?= server3

.PHONY: build install test test-short fmt vet lint vuln tidy clean dist deploy

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BIN) $(PKG)

dist:
	GOOS=$(DIST_OS) GOARCH=$(DIST_ARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_BIN) $(PKG)

# deploy copies the binary and the unit files, then asks the host whether it can
# actually reach a model. It never copies a credential: those are per host and
# live outside the repository.
deploy: dist
	ssh $(HOST) 'mkdir -p ~/bin ~/.config/taocp ~/.config/systemd/user'
	scp $(DIST_BIN) $(HOST):~/bin/$(BIN).new
	ssh $(HOST) 'mv ~/bin/$(BIN).new ~/bin/$(BIN) && chmod +x ~/bin/$(BIN)'
	scp deploy/taocp-run.service $(HOST):~/.config/systemd/user/taocp-run.service
	scp deploy/taocp-run.env.example $(HOST):~/.config/taocp/run.env.example
	ssh $(HOST) '~/bin/$(BIN) doctor'

install:
	go install -trimpath -ldflags "$(LDFLAGS)" $(PKG)

test:
	go test -race -count=1 -coverprofile=coverage.out ./...

test-short:
	go test -short ./...

fmt:
	gofmt -s -w .

vet:
	go vet ./...

lint:
	golangci-lint run

vuln:
	govulncheck ./...

tidy:
	go mod tidy

clean:
	rm -rf bin dist coverage.out
