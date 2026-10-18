GO      ?= go
BINARY  ?= lessarr
PKG     := ./cmd/lessarr

.PHONY: all build test test-integration lint fmt vet tidy image clean

all: build

build:
	$(GO) build -o bin/$(BINARY) $(PKG)

test:
	$(GO) test ./...

# Same suite, but with the tests that shell out to ffmpeg/ffprobe enabled.
# They skip themselves when the binaries are absent, so this target is mostly
# documentation of intent.
test-integration:
	$(GO) test -count=1 ./...

lint: fmt vet

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

# Deliberately explicit: no :latest, and the tag comes from the caller.
image:
	@test -n "$(TAG)" || (echo "usage: make image TAG=1.0.0" >&2; exit 2)
	docker build --platform linux/amd64 -t ghcr.io/rashidkpc/lessarr:$(TAG) .

clean:
	rm -rf bin
