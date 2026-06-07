SHELL := /bin/bash

GO ?= go
MODULES := agent-cli go-agent-loop go-llm-gateway
BUILD_CGO_ENABLED ?= 0
AGENT_CLI_OUTPUT ?= agent-cli/bin/agent
GO_TEST_TIMEOUT ?= 120s
COVERAGE_DIR ?= coverage

.DEFAULT_GOAL := help

.PHONY: help fmt vet test build coverage clean

help: ## Show available targets.
	@awk 'BEGIN {FS = ":.*## "; printf "Available targets:\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format all Go packages in workspace modules.
	@set -euo pipefail; \
	for module in $(MODULES); do \
		echo "==> fmt $$module"; \
		(cd "$$module" && $(GO) fmt ./...); \
	done

vet: ## Run go vet across all workspace modules.
	@set -euo pipefail; \
	for module in $(MODULES); do \
		echo "==> vet $$module"; \
		(cd "$$module" && $(GO) vet ./...); \
	done

test: ## Run deterministic Go tests across all workspace modules.
	@set -euo pipefail; \
	for module in $(MODULES); do \
		echo "==> test $$module"; \
		(cd "$$module" && $(GO) test ./... -timeout $(GO_TEST_TIMEOUT)); \
	done

build: ## Build the agent-cli binary and compile library packages.
	@set -euo pipefail; \
	echo "==> build agent-cli binary"; \
	mkdir -p "$$(dirname "$(AGENT_CLI_OUTPUT)")"; \
	(cd agent-cli && CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) build -o ../$(AGENT_CLI_OUTPUT) ./cmd/agent); \
	echo "==> build go-agent-loop packages"; \
	(cd go-agent-loop && CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) build ./...); \
	echo "==> build go-llm-gateway packages"; \
	(cd go-llm-gateway && CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) build ./...)

coverage: ## Write per-module coverage profiles under coverage/.
	@set -euo pipefail; \
	mkdir -p "$(COVERAGE_DIR)"; \
	for module in $(MODULES); do \
		echo "==> coverage $$module"; \
		(cd "$$module" && $(GO) test ./... -timeout $(GO_TEST_TIMEOUT) -coverprofile="../$(COVERAGE_DIR)/$$module.out"); \
	done

clean: ## Remove root-generated build and coverage outputs.
	rm -rf "$(COVERAGE_DIR)" "$(AGENT_CLI_OUTPUT)"
