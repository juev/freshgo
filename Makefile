VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test test-integration bench lint

build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/freshgo ./cmd/freshgo

test:
	go test -race ./...

# Needs Docker: starts a throwaway PostgreSQL.
test-integration:
	./scripts/test-integration.sh

# Fails when a search of 100 000 entries takes longer than it may. Runs on
# SQLite, and on PostgreSQL too when FRESHGO_TEST_POSTGRES_URL names a server.
bench:
	go test -run '^$$' -bench . -benchtime 3x -timeout 30m ./internal/store

lint:
	go vet ./...
	golangci-lint run ./...
