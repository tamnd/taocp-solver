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

.PHONY: build install test test-short fmt vet lint vuln tidy clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BIN) $(PKG)

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
