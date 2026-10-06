VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test test-integration lint

build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/freshgo ./cmd/freshgo

test:
	go test -race ./...

# Needs Docker: starts a throwaway PostgreSQL.
test-integration:
	./scripts/test-integration.sh

lint:
	go vet ./...
	golangci-lint run ./...
