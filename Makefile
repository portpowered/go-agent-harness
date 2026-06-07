SHELL := /bin/bash

GO ?= go
MODULES := agent-cli go-agent-loop go-llm-gateway
BUILD_CGO_ENABLED ?= 0
AGENT_CLI_OUTPUT ?= agent-cli/bin/agent
GO_TEST_TIMEOUT ?= 120s
COVERAGE_DIR ?= coverage
CUSTOMER_SESSION_DIR ?= $(HOME)/.codex/sessions
GOLANGCI_LINT ?= golangci-lint
STATICCHECK ?= staticcheck
GOLANGCI_LINT_INSTALL ?= go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
STATICCHECK_INSTALL ?= go install honnef.co/go/tools/cmd/staticcheck@latest
AGENT_CLI_INTEGRATION_PACKAGE := ./test/integration
GO_AGENT_LOOP_FUNCTIONAL_PACKAGE := ./test/functional
AGENT_CLI_REGRESSION_TESTS := TestRecordReplayStateless|TestRecordReplaySession|TestSessionReplayFixture_.*|TestSessionCommand_Replay.*|TestSessionCommand_OpenAIRealtimeReplay.*|TestReplayStreaming_2_2
GO_LLM_GATEWAY_REGRESSION_PACKAGES := ./internal/sessionfixturevalidator ./pkg/testing ./pkg/providers/anthropic ./pkg/providers/gemini ./pkg/providers/openai

.DEFAULT_GOAL := help

.PHONY: help fmt vet lint staticcheck test test-integration test-regressions test-customer-sessions build coverage clean

help: ## Show available targets.
	@awk 'BEGIN {FS = ":.*## "; printf "Available targets:\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nOptional skip env vars:\n"
	@printf "  %-18s %s\n" "SKIP_LINT=1" "Skip golangci-lint with a visible message."
	@printf "  %-18s %s\n" "SKIP_STATICCHECK=1" "Skip staticcheck with a visible message."
	@printf "\nOpt-in test env vars:\n"
	@printf "  %-18s %s\n" "RUN_CUSTOMER_SESSIONS=1" "Acknowledge local-only private session sweep targets."
	@printf "  %-18s %s\n" "CUSTOMER_SESSION_DIR=..." "Override the private session directory checked by test-customer-sessions."

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

lint: ## Run golangci-lint across all workspace modules.
	@set -euo pipefail; \
	if [ "$${SKIP_LINT:-0}" = "1" ]; then \
		echo "==> lint skipped via SKIP_LINT=1"; \
		exit 0; \
	fi; \
	if ! command -v "$(GOLANGCI_LINT)" >/dev/null 2>&1; then \
		echo "golangci-lint is required for 'make lint'."; \
		echo "Install it with: $(GOLANGCI_LINT_INSTALL)"; \
		echo "Or rerun with SKIP_LINT=1 to skip intentionally."; \
		exit 1; \
	fi; \
	for module in $(MODULES); do \
		echo "==> lint $$module"; \
		(cd "$$module" && "$(GOLANGCI_LINT)" run ./...); \
	done

staticcheck: ## Run staticcheck across all workspace modules.
	@set -euo pipefail; \
	if [ "$${SKIP_STATICCHECK:-0}" = "1" ]; then \
		echo "==> staticcheck skipped via SKIP_STATICCHECK=1"; \
		exit 0; \
	fi; \
	if ! command -v "$(STATICCHECK)" >/dev/null 2>&1; then \
		echo "staticcheck is required for 'make staticcheck'."; \
		echo "Install it with: $(STATICCHECK_INSTALL)"; \
		echo "Or rerun with SKIP_STATICCHECK=1 to skip intentionally."; \
		exit 1; \
	fi; \
	for module in $(MODULES); do \
		echo "==> staticcheck $$module"; \
		(cd "$$module" && "$(STATICCHECK)" ./...); \
	done

test: ## Run deterministic Go tests across all workspace modules.
	@set -euo pipefail; \
	for module in $(MODULES); do \
		echo "==> test $$module"; \
		(cd "$$module" && $(GO) test ./... -timeout $(GO_TEST_TIMEOUT)); \
	done

test-integration: ## Run deterministic integration tests for agent-cli and go-agent-loop without live credentials.
	@set -euo pipefail; \
	echo "==> test-integration agent-cli ($(AGENT_CLI_INTEGRATION_PACKAGE))"; \
	(cd agent-cli && $(GO) test $(AGENT_CLI_INTEGRATION_PACKAGE) -timeout $(GO_TEST_TIMEOUT)); \
	echo "==> test-integration go-agent-loop ($(GO_AGENT_LOOP_FUNCTIONAL_PACKAGE))"; \
	(cd go-agent-loop && $(GO) test $(GO_AGENT_LOOP_FUNCTIONAL_PACKAGE) -timeout $(GO_TEST_TIMEOUT))

test-regressions: ## Run committed replay and fixture regression tests suitable for CI.
	@set -euo pipefail; \
	echo "==> test-regressions agent-cli replay fixtures"; \
	(cd agent-cli && $(GO) test $(AGENT_CLI_INTEGRATION_PACKAGE) -run '$(AGENT_CLI_REGRESSION_TESTS)' -timeout $(GO_TEST_TIMEOUT)); \
	echo "==> test-regressions go-llm-gateway replay fixtures"; \
	(cd go-llm-gateway && $(GO) test $(GO_LLM_GATEWAY_REGRESSION_PACKAGES) -timeout $(GO_TEST_TIMEOUT))

test-customer-sessions: ## Inspect private session data only when explicitly opted in; otherwise skip with guidance.
	@set -euo pipefail; \
	if [ "$${RUN_CUSTOMER_SESSIONS:-0}" != "1" ]; then \
		echo "==> test-customer-sessions skipped: set RUN_CUSTOMER_SESSIONS=1 to acknowledge the local-only private session sweep."; \
		echo "   expected session directory: $(CUSTOMER_SESSION_DIR)"; \
		exit 0; \
	fi; \
	if [ ! -d "$(CUSTOMER_SESSION_DIR)" ]; then \
		echo "==> test-customer-sessions skipped: private session directory not found at $(CUSTOMER_SESSION_DIR)."; \
		echo "   Populate CUSTOMER_SESSION_DIR or unset RUN_CUSTOMER_SESSIONS to keep deterministic local/CI runs."; \
		exit 0; \
	fi; \
	echo "==> test-customer-sessions placeholder: private session sweep for $(CUSTOMER_SESSION_DIR) is reserved for later Phase 3 work."; \
	echo "   No checks ran against private session data in this phase."; \
	exit 0

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
