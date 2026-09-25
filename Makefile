SHELL := /bin/bash

GO ?= go
MODULES := agent-cli go-agent-loop go-llm-gateway go-audio go-device-gateway go-agent-runtime
COVERAGE_LIBRARY_MODULES := $(filter-out agent-cli,$(MODULES))
LINT_MODULES := $(MODULES) tests/embedding tools/architecturegate tools/analyzergate tools/coveragegate tools/rtc-race-gate tools/session-race-gate tools/timingate scripts/webmcp-o0 test/localai
LINT_BASE ?= origin/main
# Hard limits for all code, then linters with legacy debt for new code only.
LINT_CONFIG ?= .golangci.yml
LINT_NEW_CONFIG ?= .golangci.new.yml
BUILD_CGO_ENABLED ?= 0
AGENT_CLI_OUTPUT ?= agent-cli/bin/yui
AGENT_AUDIO_DEVICE_SERVER_OUTPUT ?= agent-cli/bin/audio-device-server
GO_TEST_TIMEOUT ?= 300s
AGENT_CLI_INTEGRATION_TIMEOUT ?= 480s
# agent-cli/test/integration is the slowest package; it is compiled once and
# its top-level tests run as disjoint shards (scripts/go-test-shards.sh).
AGENT_CLI_INTEGRATION_SHARDS ?= 5
# Recorded test durations that balance the shards (path relative to agent-cli).
AGENT_CLI_INTEGRATION_WEIGHTS := test/integration/testdata/shard-weights.txt
# Local runs of every shard execute at most this many at once. All shards run
# together: with the shared pre-warmed binaries and the accelerated audio
# drain they pass on a workstation at load average ~25; lower it if a
# timing-bound test is starved on a smaller machine.
AGENT_CLI_INTEGRATION_JOBS ?= $(AGENT_CLI_INTEGRATION_SHARDS)
# Every shard process of one run reuses a single build of the integration
# package's process-boundary binaries (agent, audio-device-server, mock tool
# agent) through this directory instead of linking them once per shard.
AGENT_CLI_INTEGRATION_SHARED_DIR_ENV := AGENT_CLI_INTEGRATION_SHARED_DIR
AGENT_CLI_INTEGRATION_SHARD_ARGS = --go "$(GO)" --dir . --package $(AGENT_CLI_INTEGRATION_PACKAGE) --shards $(AGENT_CLI_INTEGRATION_SHARDS) --weights $(AGENT_CLI_INTEGRATION_WEIGHTS) --jobs $(AGENT_CLI_INTEGRATION_JOBS) --shared-dir-env $(AGENT_CLI_INTEGRATION_SHARED_DIR_ENV)
# Independent module test runs (make test, test-hermetic, coverage) execute at
# most this many at once, longest first: agent-cli takes one slot and the
# library modules rotate through the others.
TEST_MODULE_JOBS ?= 3
MODULE_SCHEDULE := agent-cli go-agent-runtime go-agent-loop go-llm-gateway go-audio go-device-gateway
# scheduled_modules orders a module list longest-first for scripts/run-bounded.sh.
scheduled_modules = $(filter $(1),$(MODULE_SCHEDULE)) $(filter-out $(MODULE_SCHEDULE),$(1))
# Local coverage runs may reuse Go's test result cache (coverage profiles are
# cacheable), so an unchanged package is not re-run on every prepush. CI and
# COVERAGE_COUNT=1 force a fresh run of every package.
COVERAGE_COUNT ?= $(if $(filter true 1,$(CI)),1,)
COVERAGE_COUNT_FLAG = $(if $(COVERAGE_COUNT),-count=$(COVERAGE_COUNT),)
# Which part of the agent-cli coverage corpus to run: all, unit (every package
# except test/integration), or integration-K for shard K of the integration
# package. CI runs each part as a separate matrix job.
AGENT_CLI_COVERAGE_SHARD ?= all
AGENT_CLI_COVERPKG := github.com/portpowered/go-agent-harness/agent-cli/...,github.com/portpowered/go-agent-harness/go-agent-runtime/...
AGENT_CLI_INTEGRATION_PROFILES = $(foreach shard,$(shell seq 1 $(AGENT_CLI_INTEGRATION_SHARDS)),$(abspath $(COVERAGE_DIR))/agent-cli-integration-$(shard).out)
AGENT_CLI_TEST_RUNNER := ./cmd/testtimeout
COVERAGE_DIR ?= coverage
COVERAGE_MANIFEST_DIR ?= coverage-manifest
COVERAGE_BASE ?= origin/main
COVERAGE_MODULES ?= $(MODULES)
COVERAGE_INCLUDE_EMBEDDING ?= 1
COVERAGE_RUN_GATE ?= 1
CUSTOMER_SESSION_DIR ?= $(HOME)/.codex/sessions
GOLANGCI_LINT ?= golangci-lint
STATICCHECK ?= staticcheck
ANALYZER_TOOL_DIR ?= .cache/go-tools
ARCHITECTURE_POLICY := docs/architecture/architecture-policy.json
ARCHITECTURE_BASELINE := docs/architecture/baselines
ARCHITECTURE_BASE ?= origin/main
GORELEASER ?= goreleaser
RTC_RACE_TIMEOUT ?= 30s
SESSIONS_RACE_TIMEOUT ?= 600s
GOLANGCI_LINT_VERSION ?= v2.9.0
STATICCHECK_VERSION ?= 2026.1
GOLANGCI_LINT_PACKAGE ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint
STATICCHECK_PACKAGE ?= honnef.co/go/tools/cmd/staticcheck
GOLANGCI_LINT_INSTALL ?= go install $(GOLANGCI_LINT_PACKAGE)@$(GOLANGCI_LINT_VERSION)
STATICCHECK_INSTALL ?= go install $(STATICCHECK_PACKAGE)@$(STATICCHECK_VERSION)
GORELEASER_INSTALL ?= go install github.com/goreleaser/goreleaser/v2@v2.17.0
PREPUSH_MAKE ?= $(MAKE)
AGENT_CLI_INTEGRATION_PACKAGE := ./test/integration
GO_AGENT_LOOP_FUNCTIONAL_PACKAGE := ./test/functional/...
AGENT_CLI_REGRESSION_TESTS := TestRecordReplayStateless|TestRecordReplaySession|TestSessionReplayFixture_.*|TestSessionCommand_Replay.*|TestSessionCommand_OpenAIRealtimeReplay.*|TestAgentBinaryOpenAIServerVADBargeInUsesRemoteAudioDevice|TestReplayStreaming_2_2
GO_LLM_GATEWAY_REGRESSION_PACKAGES := ./internal/sessionfixturevalidator ./pkg/testing ./pkg/providers/anthropic ./pkg/providers/gemini ./pkg/providers/openai
FACTORY_TEST_MODULES := factory.scripts.tests.test_setup_workspace factory.scripts.tests.test_validate_worktree_hygiene_convergence factory.scripts.tests.test_prepush_target factory.scripts.tests.test_ci_wait factory.scripts.tests.test_project_admission factory.scripts.tests.test_project_control factory.scripts.tests.test_factory_graph factory.scripts.tests.test_reconcile_projects factory.scripts.tests.test_fresh_board factory.scripts.tests.test_worktree_cleanup
RELEASE_VERSION ?= v0.0.2
RELEASE_TAGS := $(RELEASE_VERSION) $(MODULES:%=%/$(RELEASE_VERSION))
GORELEASER_CONFIG ?= .goreleaser.yaml
SKIP_RELEASE_CI ?= 0

# agent_cli_split_tests runs agent-cli as two concurrent halves: every package
# except test/integration through `go test`, and test/integration compiled
# once and run as $(AGENT_CLI_INTEGRATION_SHARDS) concurrent shards. Together
# they execute exactly the `go test ./...` corpus.
# $(1): go build flags shared by listing and testing (e.g. -tags=nomicrophone).
# $(2): environment assignments for both halves (e.g. CGO_ENABLED=0).
define agent_cli_split_tests
(cd agent-cli; \
	packages="$$($(2) $(GO) list $(1) ./... | grep -v '/test/integration$$')"; \
	$(2) $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" --label "agent-cli packages" -- $(GO) test $$packages $(1) -timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" & unit_pid=$$!; \
	integration_status=0; \
	$(2) $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" --label "agent-cli integration shards" -- bash ../scripts/go-test-shards.sh $(AGENT_CLI_INTEGRATION_SHARD_ARGS) $(foreach flag,$(1),--build-flag $(flag)) -- -test.timeout=$(AGENT_CLI_INTEGRATION_TIMEOUT) || integration_status=$$?; \
	wait "$$unit_pid"; \
	exit "$$integration_status")
endef

.DEFAULT_GOAL := help
.PHONY: architecture-check size-check architecture-size-check test-architecture-gate verify-architecture embed-check
.PHONY: help deps fmt fmt-fix wire-check typecheck vet lint staticcheck test test-module coverage-module test-tools test-audio-stability test-audio-stability-race test-audio-device-server-integration test-rtc-race test-sessions-race test-factory-scripts test-integration test-regressions test-customer-sessions build coverage coverage-ci-agent-cli coverage-agent-cli-shard coverage-ci-libraries coverage-gate coverage-registration coverage-changed check-ci-test-partition prepush validate ci release-check release-tags release-push release-dry-run release clean test-budget test-hermetic

help: ## Show available targets.
	@awk 'BEGIN {FS = ":.*## "; printf "Available targets:\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nOptional skip env vars:\n"
	@printf "  %-18s %s\n" "SKIP_LINT=1" "Skip golangci-lint with a visible message."
	@printf "  %-18s %s\n" "SKIP_STATICCHECK=1" "Skip staticcheck with a visible message."
	@printf "  %-18s %s\n" "ANALYZER_TOOL_DIR=..." "Cache automatically installed pinned analyzers in this directory."
	@printf "  %-18s %s\n" "COVERAGE_BASE=..." "Git ref used as the changed-package coverage comparison base."
	@printf "\nOpt-in test env vars:\n"
	@printf "  %-18s %s\n" "RUN_CUSTOMER_SESSIONS=1" "Acknowledge local-only private session sweep targets."
	@printf "  %-18s %s\n" "CUSTOMER_SESSION_DIR=..." "Override the private session directory checked by test-customer-sessions."
	@printf "  %-18s %s\n" "AGENT_CLI_INTEGRATION_TIMEOUT=..." "Override the finite timeout for agent-cli/test/integration root-target invocations."
	@printf "\nRelease env vars:\n"
	@printf "  %-18s %s\n" "RELEASE_VERSION=v0.0.2" "Version used by release targets."
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
	for module in $(LINT_MODULES); do \
		echo "==> fmt $$module"; \
		output="$$(cd "$$module" && find . -name '*.go' -not -path './vendor/*' -exec gofmt -l {} + | sort)"; \
		if [ -n "$$output" ]; then \
			echo "gofmt drift detected in $$module:"; \
			echo "$$output"; \
			echo "Run 'make fmt-fix' to rewrite files before rerunning 'make prepush'."; \
			exit 1; \
		fi; \
	done

fmt-fix: ## Rewrite Go files in workspace modules with gofmt.
	@set -euo pipefail; \
	for module in $(LINT_MODULES); do \
		echo "==> fmt-fix $$module"; \
		(cd "$$module" && find . -name '*.go' -not -path './vendor/*' -exec gofmt -w {} +); \
	done

vet: ## Run go vet across all workspace modules.
	@set -euo pipefail; \
	for module in $(LINT_MODULES); do \
		echo "==> vet $$module"; \
		(cd "$$module" && GOWORK=off $(GO) vet ./...); \
	done

lint: ## Run golangci-lint across all workspace modules.
	@set -euo pipefail; \
	if [ "$${SKIP_LINT:-0}" = "1" ]; then \
		case "$${CI:-}" in true|1) echo "SKIP_LINT is not allowed in CI." >&2; exit 1 ;; esac; \
		echo "==> lint skipped via SKIP_LINT=1"; \
		exit 0; \
	fi; \
	if ! analyzer="$$(cd tools/analyzergate && GOWORK=off $(GO) run . \
		--tool golangci-lint \
		--expected-version "$(GOLANGCI_LINT_VERSION)" \
		--candidate "$(GOLANGCI_LINT)" \
		--go "$(GO)" \
		--install-package "$(GOLANGCI_LINT_PACKAGE)" \
		--tool-dir "$(abspath $(ANALYZER_TOOL_DIR))" \
		--working-dir "$(CURDIR)")"; then \
		echo "golangci-lint resolution failed for pinned version $(GOLANGCI_LINT_VERSION)." >&2; \
		echo "Install with: $(GOLANGCI_LINT_INSTALL)" >&2; \
		exit 1; \
	fi; \
	echo "==> lint using $$analyzer (pinned $(GOLANGCI_LINT_VERSION))"; \
	for module in $(LINT_MODULES); do \
		echo "==> lint $$module (hard limits, all code: $(LINT_CONFIG))"; \
		GOWORK=off scripts/golangci-lint-working-tree.sh --analyzer "$$analyzer" --all-code --config "$(LINT_CONFIG)" --module "$$module" --repo "$(CURDIR)" -- ./...; \
		echo "==> lint $$module (new code since $(LINT_BASE): $(LINT_NEW_CONFIG))"; \
		GOWORK=off scripts/golangci-lint-working-tree.sh --analyzer "$$analyzer" --config "$(LINT_NEW_CONFIG)" --base "$(LINT_BASE)" --module "$$module" --repo "$(CURDIR)" -- ./...; \
	done

staticcheck: ## Run staticcheck across all workspace modules.
	@set -euo pipefail; \
	if [ "$${SKIP_STATICCHECK:-0}" = "1" ]; then \
		case "$${CI:-}" in true|1) echo "SKIP_STATICCHECK is not allowed in CI." >&2; exit 1 ;; esac; \
		echo "==> staticcheck skipped via SKIP_STATICCHECK=1"; \
		exit 0; \
	fi; \
	if ! analyzer="$$(cd tools/analyzergate && GOWORK=off $(GO) run . \
		--tool staticcheck \
		--expected-version "$(STATICCHECK_VERSION)" \
		--candidate "$(STATICCHECK)" \
		--go "$(GO)" \
		--install-package "$(STATICCHECK_PACKAGE)" \
		--tool-dir "$(abspath $(ANALYZER_TOOL_DIR))" \
		--working-dir "$(CURDIR)")"; then \
		echo "staticcheck resolution failed for pinned version $(STATICCHECK_VERSION)." >&2; \
		echo "Install with: $(STATICCHECK_INSTALL)" >&2; \
		exit 1; \
	fi; \
	echo "==> staticcheck using $$analyzer (pinned $(STATICCHECK_VERSION))"; \
	for module in $(LINT_MODULES); do \
		echo "==> staticcheck $$module"; \
		(cd "$$module" && GOWORK=off "$$analyzer" ./...); \
	done

test: ## Run deterministic Go tests across all workspace modules.
	@set -euo pipefail; \
	echo "==> test $(MODULES) (at most $(TEST_MODULE_JOBS) modules at once)"; \
	bash scripts/run-bounded.sh --jobs $(TEST_MODULE_JOBS) -- $(foreach module,$(call scheduled_modules,$(MODULES)),'test $(module)::$(MAKE) --no-print-directory test-module TEST_MODULE=$(module)'); \
	$(MAKE) test-tools

# test-module runs one module's tests for test (TEST_HERMETIC unset) or
# test-hermetic (TEST_HERMETIC=1: CGO disabled and the microphone stub).
test-module:
	@set -euo pipefail; \
	module="$(TEST_MODULE)"; \
	if [ "$$module" = "agent-cli" ]; then \
		echo "==> test agent-cli (target-wide timeout for $(AGENT_CLI_INTEGRATION_PACKAGE): $(AGENT_CLI_INTEGRATION_TIMEOUT))"; \
		$(if $(TEST_HERMETIC),$(call agent_cli_split_tests,-tags=nomicrophone,CGO_ENABLED=0),$(call agent_cli_split_tests,,)); \
	else \
		echo "==> test $$module (general package timeout: $(GO_TEST_TIMEOUT))"; \
		(cd "$$module" && $(if $(TEST_HERMETIC),CGO_ENABLED=0 )$(GO) test ./... $(if $(TEST_HERMETIC),-tags=nomicrophone )-timeout "$(GO_TEST_TIMEOUT)"); \
	fi

test-tools: ## Run tests for standalone repository helper modules.
	@set -euo pipefail; \
	python3 -B -m unittest discover -s scripts -p test_check_wire.py; \
	python3 -B -m unittest scripts.test_check_ci_test_partition; \
	python3 -B -m unittest factory.scripts.tests.test_golangci_lint_working_tree; \
	echo "==> test tools/analyzergate"; \
	(cd tools/analyzergate && GOWORK=off $(GO) test ./... -timeout "$(GO_TEST_TIMEOUT)"); \
	echo "==> test tools/session-race-gate"; \
	(cd tools/session-race-gate && GOWORK=off $(GO) test ./... -timeout "$(GO_TEST_TIMEOUT)"); \
	$(MAKE) test-architecture-gate

architecture-check: ## Enforce service ownership, public contracts, and dependency direction.
	@cd tools/architecturegate && GOWORK=off $(GO) run . -repo ../.. -manifest $(ARCHITECTURE_POLICY) -baseline $(ARCHITECTURE_BASELINE) -baseline-base "$(ARCHITECTURE_BASE)" -check architecture

size-check: ## Enforce package, file, and function budgets against exact legacy debt.
	@cd tools/architecturegate && GOWORK=off $(GO) run . -repo ../.. -manifest $(ARCHITECTURE_POLICY) -baseline $(ARCHITECTURE_BASELINE) -baseline-base "$(ARCHITECTURE_BASE)" -check size

architecture-size-check: ## Enforce architecture and size budgets in one shared inventory pass.
	@cd tools/architecturegate && GOWORK=off $(GO) run . -repo ../.. -manifest $(ARCHITECTURE_POLICY) -baseline $(ARCHITECTURE_BASELINE) -baseline-base "$(ARCHITECTURE_BASE)" -check architecture,size

test-architecture-gate: ## Verify architecture enforcement against positive and negative fixtures.
	@cd tools/architecturegate && GOWORK=off $(GO) test ./... -timeout "$(GO_TEST_TIMEOUT)"

verify-architecture: architecture-size-check test-architecture-gate wire-check ## Run architecture and generated-composition checks.

embed-check: ## Exercise the public runtime API from an independent headless consumer module.
	@cd tests/embedding && GOWORK=off CGO_ENABLED=0 $(GO) test -mod=readonly ./... -count=1 -timeout "$(GO_TEST_TIMEOUT)"

test-audio-stability: ## Run deterministic duplex, queue, resampler, capsule, and RTC audio regressions.
	@set -euo pipefail; \
	(cd go-audio && $(GO) test ./... -count=1 -timeout "$(GO_TEST_TIMEOUT)"); \
	(cd go-device-gateway && $(GO) test ./... -count=1 -timeout "$(GO_TEST_TIMEOUT)"); \
	(cd agent-cli && $(GO) test ./internal/services/... -count=1 -timeout "$(GO_TEST_TIMEOUT)"); \
	(cd agent-cli && $(GO) test ./test/integration -run '^TestSessionWebMCPDeviceLoopbackRecordsAndReplaysAudio$$' -count=1 -timeout "$(GO_TEST_TIMEOUT)")

test-audio-stability-race: ## Run callback, cancellation, queue, and replay audio paths under the race detector.
	@set -euo pipefail; \
	(cd agent-cli && CGO_ENABLED=1 $(GO) test -race -tags=nomicrophone ../go-audio/... ../go-device-gateway/... ./internal/services/... \
		-run 'Test(SimulatedDuplex|RemoteDeviceServer|SessionAudioFailureCapsule|FailureCapsule|VirtualPlaybackCapacityAdversarial|RTCDeviceSinkSerializes|RTCDeviceSinkDiscard|RTCDeviceBoundSessionDrops)' \
		-count=1 -timeout "$(RTC_RACE_TIMEOUT)")

test-audio-device-server-integration: ## Build both binaries and run the process-boundary OpenAI audio replay.
	@set -euo pipefail; \
	echo "==> test-audio-device-server-integration agent + audio-device-server replay"; \
	(cd agent-cli && YUI_AUDIO_STRESS=1 $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" -- $(GO) test ./test/integration \
		-run '^Test(AgentBinaryOpenAIServerVADBargeInUsesRemoteAudioDevice|AgentBinaryAudioOutRecordsRemoteDevicePCM|AgentBinaryToolContinuationPreservesRemoteDeviceAudio|AgentBinaryTest45HighRateToolAudioRegression|AgentBinaryTest46HighRateToolAudioRegression|AudioDeviceServerBinaryDefaultClockRunsWithoutController)$$' -count=1 -timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)")

test-rtc-race: ## Run the focused RTC concurrency acceptance tests with the race detector.
	@set -euo pipefail; \
	echo "==> test-rtc-race go-llm-gateway/pkg/transport/rtc"; \
	(cd tools/rtc-race-gate && GOWORK=off CGO_ENABLED=1 $(GO) run . -go "$(GO)" -module-dir "../../go-llm-gateway" -timeout "$(RTC_RACE_TIMEOUT)")

test-sessions-race: ## Run the concurrent session capacity acceptance tests with the race detector.
	@set -euo pipefail; \
	echo "==> test-sessions-race go-agent-loop/test/functional/sessions"; \
	(cd tools/session-race-gate && GOWORK=off CGO_ENABLED=1 $(GO) run . -go "$(GO)" -module-dir "../../go-agent-loop" -timeout "$(SESSIONS_RACE_TIMEOUT)")

test-factory-scripts: ## Run deterministic factory script tests without writing Python bytecode into the repo checkout.
	@set -euo pipefail; \
	echo "==> test-factory-scripts modules: $(FACTORY_TEST_MODULES)"; \
	if output="$$(PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest -v $(FACTORY_TEST_MODULES) 2>&1)"; then \
		status=0; \
	else \
		status=$$?; \
	fi; \
	printf '%s\n' "$$output"; \
	test_count="$$(printf '%s\n' "$$output" | sed -nE 's/^Ran ([0-9]+) tests? in .*/\1/p' | tail -n 1)"; \
	if [ "$$test_count" = "0" ]; then \
		echo "test-factory-scripts selected zero tests from $(FACTORY_TEST_MODULES)." >&2; \
		exit 1; \
	fi; \
	if [ "$$status" -ne 0 ]; then \
		echo "test-factory-scripts failed while loading or executing the selected modules." >&2; \
		exit "$$status"; \
	fi; \
	case "$$test_count" in \
		'') \
			echo "test-factory-scripts selected zero tests from $(FACTORY_TEST_MODULES)." >&2; \
			exit 1; \
			;; \
	esac; \
	echo "==> test-factory-scripts executed $$test_count tests from configured modules"; \
	if [ "$${FACTORY_TEST_CONTRACT_CHILD:-0}" = "0" ]; then \
		echo "==> test-factory-scripts command contract"; \
		FACTORY_TEST_CONTRACT_CHILD=1 PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest -v factory.scripts.tests.test_factory_script_target; \
	fi

test-integration: ## Run deterministic integration tests for agent-cli and go-agent-loop without live credentials.
	@set -euo pipefail; \
	echo "==> test-integration agent-cli ($(AGENT_CLI_INTEGRATION_PACKAGE), timeout $(AGENT_CLI_INTEGRATION_TIMEOUT)) and go-agent-loop ($(GO_AGENT_LOOP_FUNCTIONAL_PACKAGE), timeout $(GO_TEST_TIMEOUT)) concurrently"; \
	bash scripts/run-bounded.sh -- \
		'test-integration agent-cli::cd agent-cli && $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" -- bash ../scripts/go-test-shards.sh $(AGENT_CLI_INTEGRATION_SHARD_ARGS) -- -test.timeout=$(AGENT_CLI_INTEGRATION_TIMEOUT)' \
		'test-integration go-agent-loop::cd go-agent-loop && $(GO) test $(GO_AGENT_LOOP_FUNCTIONAL_PACKAGE) -timeout "$(GO_TEST_TIMEOUT)"'

test-regressions: ## Run committed replay and fixture regression tests suitable for CI.
	@set -euo pipefail; \
	echo "==> test-regressions agent-cli replay fixtures ($(AGENT_CLI_INTEGRATION_PACKAGE), timeout $(AGENT_CLI_INTEGRATION_TIMEOUT))"; \
	(cd agent-cli && $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" -- $(GO) test $(AGENT_CLI_INTEGRATION_PACKAGE) -run '$(AGENT_CLI_REGRESSION_TESTS)' -timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)"); \
	echo "==> test-regressions go-llm-gateway replay fixtures (timeout $(GO_TEST_TIMEOUT))"; \
	(cd go-llm-gateway && $(GO) test $(GO_LLM_GATEWAY_REGRESSION_PACKAGES) -timeout "$(GO_TEST_TIMEOUT)")

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
	(cd agent-cli && CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) build -o ../$(AGENT_CLI_OUTPUT) ./cmd/yui); \
	echo "==> build deterministic audio-device server"; \
	(cd agent-cli && CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) build -o ../$(AGENT_AUDIO_DEVICE_SERVER_OUTPUT) ./cmd/audio-device-server); \
	for module in $(filter-out agent-cli,$(MODULES)); do \
		echo "==> build $$module packages"; \
		(cd "$$module" && CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) build ./...); \
	done; \
	echo "==> build tools/analyzergate"; \
	analyzer_build_dir="$$(mktemp -d)"; \
	trap 'rm -rf "$$analyzer_build_dir"' EXIT; \
	(cd tools/analyzergate && GOWORK=off CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) build -o "$$analyzer_build_dir/analyzergate" .)

typecheck: build ## Backward-compatible alias for root compile validation.

coverage: ## Write per-module coverage profiles under coverage/.
	@set -euo pipefail; \
	mkdir -p "$(COVERAGE_DIR)"; \
	echo "==> coverage $(COVERAGE_MODULES)$(if $(filter 1,$(COVERAGE_INCLUDE_EMBEDDING)), embedding) (at most $(TEST_MODULE_JOBS) at once, test count flag '$(COVERAGE_COUNT_FLAG)')"; \
	bash scripts/run-bounded.sh --jobs $(TEST_MODULE_JOBS) -- \
		$(foreach module,$(call scheduled_modules,$(COVERAGE_MODULES)),'coverage $(module)::$(MAKE) --no-print-directory coverage-module COVERAGE_MODULE=$(module)') \
		$(if $(filter 1,$(COVERAGE_INCLUDE_EMBEDDING)),'coverage embedding::$(MAKE) --no-print-directory coverage-module COVERAGE_MODULE=embedding'); \
	if [ "$(COVERAGE_RUN_GATE)" = "1" ]; then \
		$(MAKE) coverage-gate; \
	fi

# coverage-module writes one module's profile for coverage.
coverage-module:
	@set -euo pipefail; \
	case "$(COVERAGE_MODULE)" in \
		agent-cli) \
			echo "==> coverage agent-cli (target-wide timeout for $(AGENT_CLI_INTEGRATION_PACKAGE): $(AGENT_CLI_INTEGRATION_TIMEOUT))"; \
			$(MAKE) --no-print-directory coverage-agent-cli-shard AGENT_CLI_COVERAGE_SHARD="$(AGENT_CLI_COVERAGE_SHARD)" ;; \
		embedding) \
			echo "==> embedded runtime coverage"; \
			(cd tests/embedding && GOWORK=off CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) test -mod=readonly ./... $(COVERAGE_COUNT_FLAG) -tags=nomicrophone -timeout "$(GO_TEST_TIMEOUT)" -coverpkg=github.com/portpowered/go-agent-harness/go-agent-runtime/... -coverprofile="$(abspath $(COVERAGE_DIR))/embedding.out") ;; \
		*) \
			echo "==> coverage $(COVERAGE_MODULE) (general package timeout: $(GO_TEST_TIMEOUT))"; \
			(cd "$(COVERAGE_MODULE)" && CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) test ./... $(COVERAGE_COUNT_FLAG) -tags=nomicrophone -timeout "$(GO_TEST_TIMEOUT)" -coverpkg=./... -coverprofile="$(abspath $(COVERAGE_DIR))/$(COVERAGE_MODULE).out") ;; \
	esac

coverage-ci-agent-cli: ## Write the hermetic agent-cli profile(s) for AGENT_CLI_COVERAGE_SHARD (CI matrix shard).
	@$(MAKE) coverage COVERAGE_MODULES=agent-cli COVERAGE_INCLUDE_EMBEDDING=0 COVERAGE_RUN_GATE=0 AGENT_CLI_COVERAGE_SHARD="$(AGENT_CLI_COVERAGE_SHARD)"

# unit writes coverage/agent-cli.out; integration-K writes
# coverage/agent-cli-integration-K.out; all runs the unit packages and then
# the integration shards (coverage-instrumented runs are too heavy to overlap
# on a workstation without disturbing timing-bound tests).
coverage-agent-cli-shard:
	@set -euo pipefail; \
	mkdir -p "$(COVERAGE_DIR)"; \
	shard="$(AGENT_CLI_COVERAGE_SHARD)"; \
	run_unit() { \
		(cd agent-cli && packages="$$(CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) list -tags=nomicrophone ./... | grep -v '/test/integration$$')" && \
			CGO_ENABLED=$(BUILD_CGO_ENABLED) YUI_AUDIO_STRESS=1 $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" --report-budget --label "agent-cli coverage (unit packages)" -- \
			$(GO) test $$packages $(COVERAGE_COUNT_FLAG) -tags=nomicrophone -timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" -coverpkg=$(AGENT_CLI_COVERPKG) -coverprofile="$(abspath $(COVERAGE_DIR))/agent-cli.out"); \
	}; \
	run_integration() { \
		(cd agent-cli && CGO_ENABLED=$(BUILD_CGO_ENABLED) YUI_AUDIO_STRESS=1 $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" --report-budget --label "agent-cli coverage (integration $${1:-all})" -- \
			bash ../scripts/go-test-shards.sh $(AGENT_CLI_INTEGRATION_SHARD_ARGS) $${1:+--shard "$$1"} \
			--build-flag -tags=nomicrophone --build-flag -coverpkg=$(AGENT_CLI_COVERPKG) --cover-prefix "$(abspath $(COVERAGE_DIR))/agent-cli-integration" -- -test.timeout=$(AGENT_CLI_INTEGRATION_TIMEOUT)); \
	}; \
	echo "==> coverage agent-cli shard $$shard"; \
	case "$$shard" in \
		all) run_unit; run_integration ;; \
		unit) run_unit ;; \
		integration-*) run_integration "$${shard#integration-}" ;; \
		*) echo "AGENT_CLI_COVERAGE_SHARD must be all, unit, or integration-K, got $$shard" >&2; exit 2 ;; \
	esac

coverage-ci-libraries: ## Write hermetic library and embedding profiles owned by the CI coverage shard.
	@$(MAKE) coverage COVERAGE_MODULES="$(COVERAGE_LIBRARY_MODULES)" COVERAGE_INCLUDE_EMBEDDING=1 COVERAGE_RUN_GATE=0

coverage-gate: ## Enforce coverage policy against a complete set of generated profiles.
	@echo "==> coverage gate"
	@cd tools/coveragegate && GOWORK=off CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) run . --manifest "$(abspath $(COVERAGE_MANIFEST_DIR))" $(foreach module,$(MODULES),$(abspath $(COVERAGE_DIR))/$(module).out) $(AGENT_CLI_INTEGRATION_PROFILES) $(abspath $(COVERAGE_DIR))/embedding.out

coverage-registration: ## Validate every workspace Go package is registered without running coverage.
	@set -euo pipefail; \
	echo "==> coverage-registration"; \
	(cd tools/coveragegate && GOWORK=off CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) run . \
		--validate-registration \
		--manifest "$(abspath $(COVERAGE_MANIFEST_DIR))" \
		$(foreach module,$(MODULES),--module-dir ../../$(module)))

# Service floors include callers in the CLI and embedding suite. Running only
# changed packages drops that behavioral coverage and reports false regressions.
coverage-changed: coverage ## Compatibility alias for the complete behavioral coverage gate.

wire-check: ## Regenerate the pinned Wire graph and reject generated-code drift.
	@python3 -B scripts/check-wire.py --go "$(GO)" $(MODULES)

check-ci-test-partition: ## Verify each Linux package corpus has exactly one CI owner.
	@python3 -B scripts/check-ci-test-partition.py .github/workflows/ci.yml

prepush: ## Run the fail-fast, timed local pre-push gate.
	@PREPUSH_MAKE="$(PREPUSH_MAKE)" scripts/prepush.sh

ci: ## Run the full deterministic validation pipeline used by contributors and CI.
	@set -euo pipefail; \
	steps="fmt verify-architecture vet lint staticcheck check-ci-test-partition test-tools test-factory-scripts build coverage"; \
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

release-tags: ## Create local root and per-module tags for Go module publication.
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
	@set -euo pipefail; \
	for tag in $(RELEASE_TAGS); do \
		echo "==> release-push $$tag"; \
		git push origin "$$tag"; \
	done

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

test-budget: ## Run the PR-tier test scopes and enforce the package-time budget.
	@set -euo pipefail; \
	timingate_input="$$(mktemp)"; \
	trap 'rm -f "$$timingate_input"' EXIT; \
	run_budget_test() { \
		module="$$1"; \
		shift; \
		effective_timeout="$(GO_TEST_TIMEOUT)"; \
		if [ "$$module" = "agent-cli" ] && [ "$${1:-}" = "$(AGENT_CLI_INTEGRATION_PACKAGE)" ]; then \
			effective_timeout="$(AGENT_CLI_INTEGRATION_TIMEOUT)"; \
		fi; \
		echo "==> test-budget $$module $$* (timeout $$effective_timeout)"; \
		run_budget_output="$$(mktemp)"; \
		status=0; \
		if [ "$$module" = "agent-cli" ]; then \
			(cd "$$module" && CGO_ENABLED=0 $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$$effective_timeout" -- $(GO) test "$$@" -json -count=1 -tags=nomicrophone -timeout "$$effective_timeout") >"$$run_budget_output" 2>&1 || status=$$?; \
		else \
			(cd "$$module" && CGO_ENABLED=0 $(GO) test "$$@" -json -count=1 -tags=nomicrophone -timeout "$$effective_timeout") >"$$run_budget_output" 2>&1 || status=$$?; \
		fi; \
		if [ "$$status" -eq 0 ]; then \
			cat "$$run_budget_output" >> "$$timingate_input"; \
		else \
			cat "$$run_budget_output"; \
			rm -f "$$run_budget_output"; \
			return $$status; \
		fi; \
		rm -f "$$run_budget_output"; \
	}; \
	run_budget_unit() { \
		module="$$1"; \
		packages="$$(cd "$$module" && CGO_ENABLED=0 $(GO) list ./... | grep -v '/test/')"; \
		run_budget_test "$$module" $$packages; \
	}; \
	run_budget_unit agent-cli; \
	run_budget_unit go-agent-loop; \
	run_budget_unit go-llm-gateway; \
	run_budget_unit go-audio; \
	run_budget_unit go-device-gateway; \
	run_budget_unit go-agent-runtime; \
	run_budget_test agent-cli ./test/integration; \
	run_budget_test go-agent-loop $(GO_AGENT_LOOP_FUNCTIONAL_PACKAGE); \
	run_budget_test agent-cli ./test/integration -run '$(AGENT_CLI_REGRESSION_TESTS)'; \
	run_budget_test go-llm-gateway $(GO_LLM_GATEWAY_REGRESSION_PACKAGES); \
	echo "==> test-budget evaluating package timing"; \
	(cd tools/timingate && GOWORK=off $(GO) run . < "$$timingate_input")

test-hermetic: ## Run all Go tests with CGO disabled and the microphone stub.
	@set -euo pipefail; \
	echo "==> test-hermetic $(MODULES) (CGO_ENABLED=0, tags=nomicrophone, at most $(TEST_MODULE_JOBS) modules at once)"; \
	bash scripts/run-bounded.sh --jobs $(TEST_MODULE_JOBS) -- $(foreach module,$(call scheduled_modules,$(MODULES)),'test-hermetic $(module)::$(MAKE) --no-print-directory test-module TEST_MODULE=$(module) TEST_HERMETIC=1')
