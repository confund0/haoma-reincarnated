.PHONY: build test vet lint check clean

BIN := haomad
PKG := ./cmd/haomad
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

lint:
	staticcheck ./...

check: vet lint test

clean:
	rm -f $(BIN)
