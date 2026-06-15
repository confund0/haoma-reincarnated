.PHONY: build test fuzz vet lint check clean

BIN := haomad
PKG := ./cmd/haomad
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

test:
	go test ./...
	@# Short fuzz pass on the vendored bencode pkg — catches obvious
	@# crashers on hostile DHT-payload bytes in ~6s total. Found
	@# crashes auto-save to testdata/fuzz/ and become permanent
	@# regression cases in subsequent `go test` runs.
	go test -run='^$$' -fuzz='^FuzzUnmarshal$$' -fuzztime=3s ./internal/bencode/
	go test -run='^$$' -fuzz='^FuzzRoundTrip$$' -fuzztime=3s ./internal/bencode/

fuzz:
	@# Longer fuzz runs for manual deep sweeps. Default 30s per
	@# target; override with `make fuzz FUZZTIME=2m`.
	go test -run='^$$' -fuzz='^FuzzUnmarshal$$' -fuzztime=$(FUZZTIME) ./internal/bencode/
	go test -run='^$$' -fuzz='^FuzzRoundTrip$$' -fuzztime=$(FUZZTIME) ./internal/bencode/

FUZZTIME ?= 30s

vet:
	go vet ./...

lint:
	staticcheck ./...

check: vet lint test

clean:
	rm -f $(BIN)
