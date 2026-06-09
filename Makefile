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
GORELEASER ?= goreleaser
GOLANGCI_LINT_VERSION ?= v2.3.0
STATICCHECK_VERSION ?= 2025.1.1
GOLANGCI_LINT_INSTALL ?= go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
STATICCHECK_INSTALL ?= go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
GORELEASER_INSTALL ?= go install github.com/goreleaser/goreleaser/v2@latest
AGENT_CLI_INTEGRATION_PACKAGE := ./test/integration
GO_AGENT_LOOP_FUNCTIONAL_PACKAGE := ./test/functional
AGENT_CLI_REGRESSION_TESTS := TestRecordReplayStateless|TestRecordReplaySession|TestSessionReplayFixture_.*|TestSessionCommand_Replay.*|TestSessionCommand_OpenAIRealtimeReplay.*|TestReplayStreaming_2_2
GO_LLM_GATEWAY_REGRESSION_PACKAGES := ./internal/sessionfixturevalidator ./pkg/testing ./pkg/providers/anthropic ./pkg/providers/gemini ./pkg/providers/openai
RELEASE_VERSION ?= v0.0.1
RELEASE_TAGS := $(RELEASE_VERSION) $(MODULES:%=%/$(RELEASE_VERSION))
GORELEASER_CONFIG ?= .goreleaser.yaml
SKIP_RELEASE_CI ?= 0

.DEFAULT_GOAL := help

.PHONY: help deps fmt fmt-fix typecheck vet lint staticcheck test test-factory-scripts test-integration test-regressions test-customer-sessions build coverage validate ci release-check release-tags release-push release-dry-run release clean

help: ## Show available targets.
	@awk 'BEGIN {FS = ":.*## "; printf "Available targets:\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nOptional skip env vars:\n"
	@printf "  %-18s %s\n" "SKIP_LINT=1" "Skip golangci-lint with a visible message."
	@printf "  %-18s %s\n" "SKIP_STATICCHECK=1" "Skip staticcheck with a visible message."
	@printf "\nOpt-in test env vars:\n"
	@printf "  %-18s %s\n" "RUN_CUSTOMER_SESSIONS=1" "Acknowledge local-only private session sweep targets."
	@printf "  %-18s %s\n" "CUSTOMER_SESSION_DIR=..." "Override the private session directory checked by test-customer-sessions."
	@printf "\nRelease env vars:\n"
	@printf "  %-18s %s\n" "RELEASE_VERSION=v0.0.1" "Version used by release targets."
	@printf "  %-18s %s\n" "SKIP_RELEASE_CI=1" "Skip the CI pipeline inside make release."
	@printf "  %-18s %s\n" "GORELEASER=..." "Override the GoReleaser binary."

deps: ## Sync the workspace and download module dependencies for all modules.
	@set -euo pipefail; \
	echo "==> deps go.work sync"; \
	$(GO) work sync; \
	for module in $(MODULES); do \
		echo "==> deps $$module"; \
		(cd "$$module" && $(GO) mod download); \
	done

fmt: ## Validate Go formatting across all workspace modules without rewriting files.
	@set -euo pipefail; \
	for module in $(MODULES); do \
		echo "==> fmt $$module"; \
		output="$$(cd "$$module" && find . -name '*.go' -not -path './vendor/*' -exec gofmt -l {} + | sort)"; \
		if [ -n "$$output" ]; then \
			echo "gofmt drift detected in $$module:"; \
			echo "$$output"; \
			echo "Run 'make fmt-fix' to rewrite files before rerunning 'make ci'."; \
			exit 1; \
		fi; \
	done

fmt-fix: ## Rewrite Go files in workspace modules with gofmt.
	@set -euo pipefail; \
	for module in $(MODULES); do \
		echo "==> fmt-fix $$module"; \
		(cd "$$module" && find . -name '*.go' -not -path './vendor/*' -exec gofmt -w {} +); \
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

test-factory-scripts: ## Run deterministic factory script tests without writing Python bytecode into the repo checkout.
	@set -euo pipefail; \
	echo "==> test-factory-scripts factory/scripts/tests/test_setup_workspace.py"; \
	echo "==> test-factory-scripts factory/scripts/tests/test_validate_worktree_hygiene_convergence.py"; \
	PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest \
		factory/scripts/tests/test_setup_workspace.py \
		factory/scripts/tests/test_validate_worktree_hygiene_convergence.py

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

typecheck: build ## Backward-compatible alias for root compile validation.

coverage: ## Write per-module coverage profiles under coverage/.
	@set -euo pipefail; \
	mkdir -p "$(COVERAGE_DIR)"; \
	for module in $(MODULES); do \
		echo "==> coverage $$module"; \
		(cd "$$module" && $(GO) test ./... -timeout $(GO_TEST_TIMEOUT) -coverprofile="../$(COVERAGE_DIR)/$$module.out"); \
	done

ci: ## Run the full deterministic validation pipeline used by contributors and CI.
	@set -euo pipefail; \
	steps="fmt vet lint staticcheck test-factory-scripts test test-integration test-regressions build coverage"; \
	for step in $$steps; do \
		echo "==> ci $$step"; \
		$(MAKE) "$$step" || { status=$$?; echo "==> ci failed at $$step"; exit $$status; }; \
	done

validate: ci ## Backward-compatible alias for the full deterministic root validation pipeline.

release-check: ## Validate release inputs and required release tooling.
	@set -euo pipefail; \
	case "$(RELEASE_VERSION)" in \
		v[0-9]*.[0-9]*.[0-9]*) ;; \
		*) echo "RELEASE_VERSION must look like vMAJOR.MINOR.PATCH, got $(RELEASE_VERSION)"; exit 1 ;; \
	esac; \
	if ! command -v "$(GORELEASER)" >/dev/null 2>&1; then \
		echo "goreleaser is required for release targets."; \
		echo "Install it with: $(GORELEASER_INSTALL)"; \
		exit 1; \
	fi; \
	test -f "$(GORELEASER_CONFIG)"

release-tags: ## Create local v0.0.1 root and per-module tags for Go module publication.
	@set -euo pipefail; \
	if ! git diff --quiet || ! git diff --cached --quiet; then \
		echo "release-tags requires a clean worktree so tags point at committed release content."; \
		exit 1; \
	fi; \
	head="$$(git rev-parse HEAD)"; \
	for tag in $(RELEASE_TAGS); do \
		if git rev-parse -q --verify "refs/tags/$$tag" >/dev/null; then \
			tag_head="$$(git rev-list -n 1 "$$tag")"; \
			if [ "$$tag_head" != "$$head" ]; then \
				echo "tag $$tag already exists at $$tag_head, not HEAD $$head"; \
				exit 1; \
			fi; \
			echo "==> release-tags $$tag already points at HEAD"; \
		else \
			echo "==> release-tags creating $$tag"; \
			git tag -a "$$tag" -m "Release $$tag"; \
		fi; \
	done

release-push: release-tags ## Push root and per-module release tags to origin.
	git push origin $(RELEASE_TAGS)

release-dry-run: release-check ## Build release artifacts locally without publishing.
	$(GORELEASER) release --snapshot --clean --skip=publish --config "$(GORELEASER_CONFIG)"

release: release-check ## Run validation and publish the GitHub release for RELEASE_VERSION.
	@set -euo pipefail; \
	if [ "$${SKIP_RELEASE_CI:-$(SKIP_RELEASE_CI)}" != "1" ]; then \
		$(MAKE) ci; \
	else \
		echo "==> release skipping ci via SKIP_RELEASE_CI=1"; \
	fi; \
	$(MAKE) release-tags; \
	$(GORELEASER) release --clean --config "$(GORELEASER_CONFIG)"

clean: ## Remove root-generated build and coverage outputs.
	rm -rf "$(COVERAGE_DIR)" "$(AGENT_CLI_OUTPUT)" dist
