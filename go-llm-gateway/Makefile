GO ?= go

.PHONY: build test fmt vet deps clean

default: fmt build test
build:
	$(GO) build ./...

test:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

deps:
	$(GO) mod download

deps-tidy:
	$(GO) mod tidy

clean:
	$(GO) clean ./...
