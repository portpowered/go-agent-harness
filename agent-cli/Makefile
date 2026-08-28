.PHONY: build test test-timing fmt vet deps clean generate

GO ?= go
BUILD_CGO_ENABLED ?= 0
BUILD_OUTPUT ?= bin/agent
GO_TEST_TIMEOUT ?= 10s
GO_TEST_TIMING_TOP ?= 20
TEST_TIMEOUT_RUNNER ?= ./cmd/testtimeout

# Detect git worktree environment
# In a worktree, --git-common-dir points to the main repo's .git directory,
# while --git-dir points to the worktree's .git file. When they differ, we're
# in a worktree and must force a full rebuild to avoid stale build cache.
_GIT_COMMON_DIR := $(shell git rev-parse --git-common-dir 2>/dev/null)
_GIT_DIR := $(shell git rev-parse --git-dir 2>/dev/null)
IS_WORKTREE :=
ifneq ($(_GIT_COMMON_DIR),$(_GIT_DIR))
	IS_WORKTREE := 1
endif

# When in a worktree, add -a flag to force rebuild all packages
WORKTREE_FLAGS :=
ifdef IS_WORKTREE
	WORKTREE_FLAGS := -a
endif

build: export CGO_ENABLED := $(BUILD_CGO_ENABLED)
build:
	$(GO) build $(WORKTREE_FLAGS) -o $(BUILD_OUTPUT) ./cmd/agent

test:
	$(GO) run $(TEST_TIMEOUT_RUNNER) --timeout $(GO_TEST_TIMEOUT) -- $(GO) test ./... -timeout $(GO_TEST_TIMEOUT) -v

test-timing:
	$(GO) run ./cmd/testtiming --go "$(GO)" --timeout $(GO_TEST_TIMEOUT) --top $(GO_TEST_TIMING_TOP)

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

deps:
	$(GO) mod download

deps-tidy:
	$(GO) mod tidy

clean:
	rm -rf bin/
	$(GO) clean ./...

wire:
	$(GO) generate -tags=wireinject ./internal/...
	wire ./internal/...
