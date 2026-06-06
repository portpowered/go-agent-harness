GO ?= go
BUILD_CGO_ENABLED ?= 0
GO_TEST_TIMEOUT ?= 120s

.PHONY: build test test-race fmt vet deps clean

build: export CGO_ENABLED := $(BUILD_CGO_ENABLED)
build:
	$(GO) build ./...

test:
	$(GO) test ./... -timeout $(GO_TEST_TIMEOUT) -v

test-race:
	$(GO) test ./... -race -timeout 30s -v

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
