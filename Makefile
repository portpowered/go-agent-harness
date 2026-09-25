SHELL := /bin/bash

GO ?= go
MODULES := agent-cli go-agent-loop go-llm-gateway go-audio go-device-gateway go-agent-runtime
COVERAGE_LIBRARY_MODULES := $(filter-out agent-cli,$(MODULES))
LINT_MODULES := $(MODULES) tests/embedding tools/architecturegate tools/analyzergate tools/coveragegate tools/rtc-race-gate tools/session-race-gate tools/timingate scripts/webmcp-o0 test/localai
LINT_BASE ?= origin/main
# Hard limits for all code, then linters with legacy debt for new code only.
LINT_CONFIG ?= .golangci.yml
LINT_NEW_CONFIG ?= .golangci.new.yml
# LINT_SHARD selects the modules `make lint` and `make lint-cross` check:
# all (default), agent-cli, libraries (every other lint module), or one half
# of libraries: runtime (go-agent-runtime and the modules it builds on) or
# support (gateways, tools and scripts). CI runs shards in parallel lanes so
# that each lane stays under three minutes even with a cold build cache.
LINT_SHARD ?= all
LINT_LIBRARY_MODULES := $(filter-out agent-cli,$(LINT_MODULES))
LINT_RUNTIME_MODULES := go-agent-runtime go-agent-loop go-audio tests/embedding
LINT_SUPPORT_MODULES := $(filter-out $(LINT_RUNTIME_MODULES),$(LINT_LIBRARY_MODULES))
LINT_SHARD_MODULES_all := $(LINT_MODULES)
LINT_SHARD_MODULES_agent-cli := agent-cli
LINT_SHARD_MODULES_libraries := $(LINT_LIBRARY_MODULES)
LINT_SHARD_MODULES_runtime := $(LINT_RUNTIME_MODULES)
LINT_SHARD_MODULES_support := $(LINT_SUPPORT_MODULES)
LINT_SELECTED_MODULES = $(or $(LINT_SHARD_MODULES_$(LINT_SHARD)),$(error unknown LINT_SHARD '$(LINT_SHARD)'; use all, agent-cli, libraries, runtime or support))
# Parallel lint runs start the modules with the longest cold lint first so the
# slowest module does not start last.
LINT_SCHEDULE := agent-cli go-llm-gateway go-agent-runtime go-agent-loop go-device-gateway go-audio tests/embedding tools/architecturegate
LINT_SCHEDULED_MODULES = $(filter $(LINT_SELECTED_MODULES),$(LINT_SCHEDULE)) $(filter-out $(LINT_SCHEDULE),$(LINT_SELECTED_MODULES))
# Operating systems cross-linted by `make lint-cross` with cgo disabled.
LINT_CROSS_GOOS ?= windows darwin
# Extra build tags appended to the configured ones for one cross GOOS.
# nomicrophone on darwin with cgo disabled adds the files that only build
# with the tag and removes none: every `!nomicrophone` darwin file also
# needs cgo.
LINT_CROSS_TAGS_darwin := nomicrophone
# Modules `make lint` and `make lint-cross` run at once. Each golangci-lint
# run is itself parallel, but small modules are dominated by per-run startup
# and package loading, so overlapping them shortens the run.
LINT_JOBS ?= 4
# Parallel module runs share the analysis cache; skip the start-up lock.
LINT_RUN_FLAGS := --allow-parallel-runners
BUILD_CGO_ENABLED ?= 0
# `make build` also compiles every library package and tools/analyzergate.
# CI sets 0: the coverage libraries shard compiles every library package
# (-cover builds packages without tests too) and test-tools compiles
# analyzergate, so the unit job only links the agent-cli binaries.
BUILD_LIBRARY_PACKAGES ?= 1
AGENT_CLI_OUTPUT ?= agent-cli/bin/yui
AGENT_AUDIO_DEVICE_SERVER_OUTPUT ?= agent-cli/bin/audio-device-server
GO_TEST_TIMEOUT ?= 300s
AGENT_CLI_INTEGRATION_TIMEOUT ?= 480s
# agent-cli/test/integration is the slowest package; it is compiled once and
# its top-level tests run as disjoint shards (scripts/go-test-shards.sh).
# The shards wait on paced audio far more than they compute (~200s of summed
# test time on a 4-vCPU CI runner), so more shards than cores shortens the
# run until the longest single test (~25s) bounds it.
AGENT_CLI_INTEGRATION_SHARDS ?= 8
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
# The same binaries, built into that directory while the test binary
# compiles (the package's TestMain builds any that are missing, with the same
# `go build -o NAME SOURCE` from the package directory, so a name that drifts
# from TestMain's costs a rebuild, never correctness).
AGENT_CLI_INTEGRATION_PREBUILDS := agent=../../cmd/agent audio-device-server=../../cmd/audio-device-server mock-tool-agent=./testcmd/mock-tool-agent
AGENT_CLI_INTEGRATION_SHARD_ARGS = --go "$(GO)" --dir . --package $(AGENT_CLI_INTEGRATION_PACKAGE) --shards $(AGENT_CLI_INTEGRATION_SHARDS) --weights $(AGENT_CLI_INTEGRATION_WEIGHTS) --jobs $(AGENT_CLI_INTEGRATION_JOBS) --shared-dir-env $(AGENT_CLI_INTEGRATION_SHARED_DIR_ENV) $(foreach prebuild,$(AGENT_CLI_INTEGRATION_PREBUILDS),--prebuild $(prebuild))
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
# except test/integration), integration (every shard of the integration
# package, compiled once and run concurrently), or integration-K for shard K
# alone. CI runs unit and integration as separate matrix jobs.
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
# COVERAGE_SCOPE=full (default) runs every package and gates every floor.
# COVERAGE_SCOPE=changed (what `make coverage-changed` and the default local
# prepush use) runs only the test packages whose test binary links a package
# changed since COVERAGE_BASE (the reverse dependency closure, across modules
# and the embedding consumer) and gates the floors of the changed packages and
# their importers, whose every covering test is in that closure. Changes to
# go.mod/go.sum/go.work, the Makefile, the test orchestration scripts or the
# coverage gate fall back to the full scope. CI always runs the full scope.
COVERAGE_SCOPE ?= full
COVERAGE_CHANGED_DIR ?= $(COVERAGE_DIR)/changed
# Test packages (./relative, space separated) coverage-module runs for a
# library module, and the agent-cli unit packages coverage-agent-cli-shard
# runs; empty means every package. Set by coverage-changed.
COVERAGE_PACKAGES ?=
AGENT_CLI_COVERAGE_UNIT_PACKAGES ?=
# test-tools also runs the architecture gate fixtures unless the caller (the
# prepush gate, which runs them in verify-architecture) already has.
TEST_TOOLS_ARCHITECTURE_GATE ?= 1
# `go list` fields that decide which files a package builds and tests; the
# packages whose fields differ between the hermetic coverage build and the
# native build are the ones test-cgo-delta runs natively.
PACKAGE_FILES_FORMAT := {{.ImportPath}} {{.GoFiles}} {{.CgoFiles}} {{.TestGoFiles}} {{.XTestGoFiles}}
CUSTOMER_SESSION_DIR ?= $(HOME)/.codex/sessions
GOLANGCI_LINT ?= golangci-lint
STATICCHECK ?= staticcheck
ANALYZER_TOOL_DIR ?= .cache/go-tools
ARCHITECTURE_POLICY := docs/architecture/architecture-policy.json
ARCHITECTURE_BASELINE := docs/architecture/baselines
ARCHITECTURE_BASE ?= origin/main
GORELEASER ?= goreleaser
RTC_RACE_TIMEOUT ?= 30s
# Race test binaries sleep 1s at exit by default (GORACE atexit_sleep_ms);
# across the race targets' ~20 binaries that was a third of their time.
RACE_GORACE ?= atexit_sleep_ms=0
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
AGENT_CLI_REGRESSION_TESTS := TestRecordReplayStateless|TestSessionCommand_Replay.*|TestSessionCommand_OpenAIRealtimeReplay.*|TestAgentBinaryOpenAIServerVADBargeInUsesRemoteAudioDevice|TestReplayStreaming_2_2
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
.PHONY: help deps fmt fmt-fix wire-check typecheck vet lint lint-module lint-wireinject lint-cross lint-cross-module lint-darwin-cgo staticcheck test test-module coverage-module test-tools test-audio-stability test-audio-stability-race test-audio-device-server-integration test-audio-stress test-loop-race test-linux-devices-race test-rtc-race test-sessions-race test-factory-scripts test-integration test-regressions test-customer-sessions build coverage coverage-ci-agent-cli coverage-agent-cli-shard coverage-ci-libraries coverage-gate coverage-registration coverage-changed check-ci-test-partition verify-standalone-checkout prepush prepush-full test-cgo-delta validate ci release-check release-tags release-push release-dry-run release clean test-budget test-hermetic

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

# golangci_resolve resolves the pinned golangci-lint into $$analyzer, or exits
# early when SKIP_LINT=1 outside CI. A parent lint target exports the
# resolved path as LINT_ANALYZER so per-module sub-makes skip resolution.
golangci_resolve = if [ "$${SKIP_LINT:-0}" = "1" ]; then \
		case "$${CI:-}" in true|1) echo "SKIP_LINT is not allowed in CI." >&2; exit 1 ;; esac; \
		echo "==> lint skipped via SKIP_LINT=1"; \
		exit 0; \
	fi; \
	if [ -n "$${LINT_ANALYZER:-}" ]; then \
		analyzer="$$LINT_ANALYZER"; \
	elif ! analyzer="$$(cd tools/analyzergate && GOWORK=off $(GO) run . \
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
	else \
		echo "==> lint using $$analyzer (pinned $(GOLANGCI_LINT_VERSION))"; \
	fi
# golangci_run runs the working-tree lint script for $$module.
golangci_run = GOWORK=off scripts/golangci-lint-working-tree.sh --analyzer "$$analyzer" --module "$$module" --repo "$(CURDIR)"
# Inputs whose change forces the new-code pass to lint every module even when
# a module has no Go change: the configurations and the lint tooling.
LINT_NEW_RUN_INPUTS := $(LINT_CONFIG) $(LINT_NEW_CONFIG) Makefile scripts/golangci-lint-working-tree.sh scripts/run-bounded.sh
# golangci_new_run runs the new-code pass for $$module.
golangci_new_run = $(golangci_run) --config "$(LINT_NEW_CONFIG)" --base "$(LINT_BASE)" $(foreach input,$(LINT_NEW_RUN_INPUTS),--run-if-changed $(input))
# golangci_config_check loads both configurations with `golangci-lint
# linters`, which rejects unknown linters and unloadable configurations
# offline. It runs on every lint entry point, so a configuration error fails
# even when no module is linted. (`config verify` is not used: it downloads
# its JSON schema from GitHub.)
golangci_config_check = for config in "$(LINT_CONFIG)" "$(LINT_NEW_CONFIG)"; do \
		echo "==> lint config $$config"; \
		"$$analyzer" linters --config "$$config" >/dev/null; \
	done
# golangci_tagged_dirs prints ./dir for each package directory in the current
# module holding a Go file whose build constraint matches the ERE $(1).
golangci_tagged_dirs = { git grep -l -E '^//go:build .*$(1)' -- '*.go' ':!:**/testdata/**' || true; } | sed -E 's\#(^|/)[^/]*$$\#\#; s\#^\#./\#' | sort -u

lint: ## Run golangci-lint (default and opt-in test build tags, then wireinject) on LINT_SHARD modules, LINT_JOBS at once.
	@set -euo pipefail; \
	$(golangci_resolve); \
	$(golangci_config_check); \
	export LINT_ANALYZER="$$analyzer"; \
	bash scripts/run-bounded.sh --jobs $(LINT_JOBS) -- $(foreach module,$(LINT_SCHEDULED_MODULES),'lint $(module)::$(MAKE) --no-print-directory lint-module LINT_MODULE=$(module)'); \
	$(MAKE) --no-print-directory lint-wireinject

# lint-module runs both lint passes for one LINT_MODULE (used by `make lint`).
lint-module:
	@set -euo pipefail; \
	$(golangci_resolve); \
	module="$(LINT_MODULE)"; \
	echo "==> lint $$module (hard limits, all code: $(LINT_CONFIG))"; \
	$(golangci_run) --all-code --config "$(LINT_CONFIG)" -- $(LINT_RUN_FLAGS) ./...; \
	echo "==> lint $$module (new code since $(LINT_BASE): $(LINT_NEW_CONFIG))"; \
	$(golangci_new_run) -- $(LINT_RUN_FLAGS) ./...

# Wire injector files (//go:build wireinject) replace their package's
# !wireinject files, so they load in a separate pass restricted to the
# packages that contain them. Generated wire_gen.go is excluded as generated.
lint-wireinject: ## Run golangci-lint on Wire injector (wireinject) packages of LINT_SHARD modules.
	@set -euo pipefail; \
	$(golangci_resolve); \
	for module in $(LINT_SELECTED_MODULES); do \
		packages="$$(cd "$$module" && $(call golangci_tagged_dirs,wireinject))"; \
		if [ -z "$$packages" ]; then continue; fi; \
		echo "==> lint $$module wireinject packages (hard limits, all code: $(LINT_CONFIG))"; \
		$(golangci_run) --all-code --config "$(LINT_CONFIG)" -- --build-tags wireinject $$packages; \
		echo "==> lint $$module wireinject packages (new code since $(LINT_BASE): $(LINT_NEW_CONFIG))"; \
		$(golangci_new_run) -- --build-tags wireinject $$packages; \
	done

lint-cross: ## Run the hard golangci-lint pass for each LINT_CROSS_GOOS (cgo disabled) on LINT_SHARD modules, LINT_JOBS at once.
	@set -euo pipefail; \
	$(golangci_resolve); \
	$(golangci_config_check); \
	export LINT_ANALYZER="$$analyzer"; \
	bash scripts/run-bounded.sh --jobs $(LINT_JOBS) -- $(foreach goos,$(LINT_CROSS_GOOS),$(foreach module,$(LINT_SCHEDULED_MODULES),'lint $(module) GOOS=$(goos)::$(MAKE) --no-print-directory lint-cross-module LINT_MODULE=$(module) LINT_CROSS_TARGET=$(goos)'))

# lint-cross-module runs the hard pass for one LINT_MODULE and LINT_CROSS_TARGET
# GOOS with cgo disabled (used by `make lint-cross`).
lint-cross-module:
	@set -euo pipefail; \
	$(golangci_resolve); \
	module="$(LINT_MODULE)"; \
	echo "==> lint $$module GOOS=$(LINT_CROSS_TARGET) CGO_ENABLED=0$(if $(LINT_CROSS_TAGS_$(LINT_CROSS_TARGET)), +tags $(LINT_CROSS_TAGS_$(LINT_CROSS_TARGET))) (hard limits, all code: $(LINT_CONFIG))"; \
	GOOS="$(LINT_CROSS_TARGET)" CGO_ENABLED=0 $(golangci_run) --all-code --config "$(LINT_CONFIG)" -- $(LINT_RUN_FLAGS) $(if $(LINT_CROSS_TAGS_$(LINT_CROSS_TARGET)),--build-tags $(LINT_CROSS_TAGS_$(LINT_CROSS_TARGET))) ./...

lint-darwin-cgo: ## On macOS, run the hard golangci-lint pass with cgo on packages holding cgo-constrained files.
	@set -euo pipefail; \
	if [ "$$(uname -s)" != "Darwin" ]; then echo "lint-darwin-cgo must run on macOS (cgo needs the Darwin SDK)." >&2; exit 1; fi; \
	$(golangci_resolve); \
	$(golangci_config_check); \
	for module in $(LINT_MODULES); do \
		packages="$$(cd "$$module" && $(call golangci_tagged_dirs,cgo))"; \
		if [ -z "$$packages" ]; then continue; fi; \
		echo "==> lint $$module darwin cgo packages (hard limits, all code: $(LINT_CONFIG))"; \
		GOOS=darwin CGO_ENABLED=1 $(golangci_run) --all-code --config "$(LINT_CONFIG)" -- $$packages; \
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
	python3 -B -m unittest scripts.test_check_ci_test_partition scripts.test_ci_await_jobs; \
	python3 -B -m unittest factory.scripts.tests.test_golangci_lint_working_tree; \
	echo "==> test tools/analyzergate"; \
	(cd tools/analyzergate && GOWORK=off $(GO) test ./... -timeout "$(GO_TEST_TIMEOUT)"); \
	echo "==> test tools/session-race-gate"; \
	(cd tools/session-race-gate && GOWORK=off $(GO) test ./... -timeout "$(GO_TEST_TIMEOUT)"); \
	echo "==> test tools/coveragegate"; \
	(cd tools/coveragegate && GOWORK=off $(GO) test ./... -timeout "$(GO_TEST_TIMEOUT)"); \
	for module in tools/rtc-race-gate tools/timingate scripts/webmcp-o0 test/localai; do \
		echo "==> test $$module"; \
		(cd "$$module" && GOWORK=off $(GO) test ./... -timeout "$(GO_TEST_TIMEOUT)"); \
	done; \
	if [ "$(TEST_TOOLS_ARCHITECTURE_GATE)" = "1" ]; then \
		$(MAKE) test-architecture-gate; \
	fi

test-cgo-delta: ## Test natively (cgo, real microphone backend) only the packages whose files differ from the hermetic coverage build.
	@set -euo pipefail; \
	for module in $(MODULES); do \
		packages="$$(cd "$$module" && comm -13 \
			<(CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) list -tags=nomicrophone -f '$(PACKAGE_FILES_FORMAT)' ./... | LC_ALL=C sort) \
			<($(GO) list -f '$(PACKAGE_FILES_FORMAT)' ./... | LC_ALL=C sort) | cut -d' ' -f1)"; \
		if [ -n "$$packages" ]; then \
			echo "==> test-cgo-delta $$module:" $$packages; \
			(cd "$$module" && $(GO) test $$packages -timeout "$(GO_TEST_TIMEOUT)"); \
		fi; \
	done

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

# Only the packages that hold tests matching the pattern: race-compiling all
# of go-audio, go-device-gateway and agent-cli/internal/services (26 test
# binaries) to run tests from two of them cost most of the target's time.
# `make test-audio-stability-race AUDIO_STABILITY_RACE_PACKAGES="../go-audio/... ../go-device-gateway/... ./internal/services/..."`
# re-checks that no other package matches.
AUDIO_STABILITY_RACE_PACKAGES ?= ../go-device-gateway/pkg/devices ../go-device-gateway/pkg/runtime
test-audio-stability-race: ## Run callback, cancellation, queue, and replay audio paths under the race detector.
	@set -euo pipefail; \
	(cd agent-cli && CGO_ENABLED=1 GORACE="$(RACE_GORACE)" $(GO) test -race -tags=nomicrophone $(AUDIO_STABILITY_RACE_PACKAGES) \
		-run 'Test(SimulatedDuplex|RemoteDeviceServer|SessionAudioFailureCapsule|FailureCapsule|VirtualPlaybackCapacityAdversarial|RTCDeviceSinkSerializes|RTCDeviceSinkDiscard|RTCDeviceBoundSessionDrops)' \
		-count=1 -timeout "$(RTC_RACE_TIMEOUT)")

# The go-agent-loop packages whose tests drive concurrent sessions, the engine
# hot loop, participant runners and duplex turns. The six capacity tests that
# test-sessions-race gates (with its own retry and event verification) are
# skipped here so each runs once.
LOOP_RACE_PACKAGES := ./test/functional/sessions ./test/functional/duplex ./pkg/engine ./pkg/participants ./pkg/agentloop
test-loop-race: ## Run the go-agent-loop session, engine, participant, agent-loop and duplex tests with the race detector.
	@set -euo pipefail; \
	echo "==> test-loop-race go-agent-loop $(LOOP_RACE_PACKAGES)"; \
	skip="$$(cd tools/session-race-gate && GOWORK=off $(GO) run . -print-run-pattern)"; \
	(cd go-agent-loop && CGO_ENABLED=1 GORACE="$(RACE_GORACE)" $(GO) test -race -tags=nomicrophone $(LOOP_RACE_PACKAGES) -skip "$$skip" -count=1 -timeout "$(SESSIONS_RACE_TIMEOUT)")

# The native (cgo, real malgo backend) Linux device tests: the hermetic
# coverage build uses the nomicrophone stub, so no other job compiles them.
test-linux-devices-race: ## Run the native Linux cgo device backend tests with the race detector (Linux only).
	@set -euo pipefail; \
	if [ "$$($(GO) env GOOS)" != linux ]; then echo "==> test-linux-devices-race skipped: Linux only"; exit 0; fi; \
	echo "==> test-linux-devices-race go-device-gateway/pkg/devices (cgo)"; \
	(cd go-device-gateway && CGO_ENABLED=1 GORACE="$(RACE_GORACE)" $(GO) test -race ./pkg/devices -run '^TestLinux' -count=1 -timeout "$(RTC_RACE_TIMEOUT)")

test-audio-device-server-integration: ## Build both binaries and run the process-boundary OpenAI audio replay.
	@set -euo pipefail; \
	echo "==> test-audio-device-server-integration agent + audio-device-server replay"; \
	(cd agent-cli && YUI_AUDIO_STRESS=1 $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" -- $(GO) test ./test/integration \
		-run '^Test(AgentBinaryOpenAIServerVADBargeInUsesRemoteAudioDevice|AgentBinaryAudioOutRecordsRemoteDevicePCM|AgentBinaryToolContinuationPreservesRemoteDeviceAudio|AgentBinaryTest45HighRateToolAudioRegression|AgentBinaryTest46HighRateToolAudioRegression|AudioDeviceServerBinaryDefaultClockRunsWithoutController)$$' -count=1 -timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)")

# The fresh-process high-rate tool-audio stress trials (Test45/Test46, 20
# trials each per repetition) and the real-time device-cadence deliveries of
# TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio other than
# test45/captured_cadence skip unless YUI_AUDIO_STRESS=1. They hunt rare
# races rather than prove behavior, so pull requests do not run them (their
# test45/test46 topologies run once per remaining delivery in
# TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio); the scheduled
# Nightly audio stress workflow runs this target with the coverage job's
# build (hermetic tags, CGO_ENABLED=$(BUILD_CGO_ENABLED)).
AUDIO_STRESS_COUNT ?= 1
test-audio-stress: ## Run the fresh-process high-rate tool-audio stress trials (AUDIO_STRESS_COUNT repetitions).
	@set -euo pipefail; \
	echo "==> test-audio-stress Test45/Test46 high-rate tool audio (20 trials each) and device-cadence tool continuation, $(AUDIO_STRESS_COUNT) repetition(s)"; \
	(cd agent-cli && CGO_ENABLED=$(BUILD_CGO_ENABLED) YUI_AUDIO_STRESS=1 $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" -- $(GO) test ./test/integration -tags=nomicrophone \
		-run '^TestAgentBinary(Test4[56]HighRateToolAudioRegression|ToolContinuationPreservesRemoteDeviceAudio)$$' -count=$(AUDIO_STRESS_COUNT) -v -timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)")

test-rtc-race: ## Run the focused RTC concurrency acceptance tests with the race detector.
	@set -euo pipefail; \
	echo "==> test-rtc-race go-llm-gateway/pkg/transport/rtc"; \
	(cd tools/rtc-race-gate && GOWORK=off CGO_ENABLED=1 GORACE="$(RACE_GORACE)" $(GO) run . -go "$(GO)" -module-dir "../../go-llm-gateway" -timeout "$(RTC_RACE_TIMEOUT)")

test-sessions-race: ## Run the concurrent session capacity acceptance tests with the race detector.
	@set -euo pipefail; \
	echo "==> test-sessions-race go-agent-loop/test/functional/sessions"; \
	(cd tools/session-race-gate && GOWORK=off CGO_ENABLED=1 GORACE="$(RACE_GORACE)" $(GO) run . -go "$(GO)" -module-dir "../../go-agent-loop" -timeout "$(SESSIONS_RACE_TIMEOUT)")

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
	if [ "$(BUILD_LIBRARY_PACKAGES)" != "1" ]; then \
		echo "==> build library packages skipped (BUILD_LIBRARY_PACKAGES=$(BUILD_LIBRARY_PACKAGES))"; \
		exit 0; \
	fi; \
	for module in $(filter-out agent-cli,$(MODULES)); do \
		echo "==> build $$module packages"; \
		(cd "$$module" && CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) build ./...); \
	done; \
	echo "==> build tools/analyzergate"; \
	analyzer_build_dir="$$(mktemp -d)"; \
	trap 'rm -rf "$$analyzer_build_dir"' EXIT; \
	(cd tools/analyzergate && GOWORK=off CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) build -o "$$analyzer_build_dir/analyzergate" .)

typecheck: build ## Backward-compatible alias for root compile validation.

coverage: ## Write per-module coverage profiles under coverage/ (COVERAGE_SCOPE=changed: affected packages only).
	@set -euo pipefail; \
	if [ "$(COVERAGE_SCOPE)" = "changed" ]; then \
		exec $(MAKE) --no-print-directory coverage-changed; \
	fi; \
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
			(cd "$(COVERAGE_MODULE)" && CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) test $(or $(COVERAGE_PACKAGES),./...) $(COVERAGE_COUNT_FLAG) -tags=nomicrophone -timeout "$(GO_TEST_TIMEOUT)" -coverpkg=./... -coverprofile="$(abspath $(COVERAGE_DIR))/$(COVERAGE_MODULE).out") ;; \
	esac

coverage-ci-agent-cli: ## Write the hermetic agent-cli profile(s) for AGENT_CLI_COVERAGE_SHARD (CI matrix shard).
	@$(MAKE) coverage COVERAGE_MODULES=agent-cli COVERAGE_INCLUDE_EMBEDDING=0 COVERAGE_RUN_GATE=0 AGENT_CLI_COVERAGE_SHARD="$(AGENT_CLI_COVERAGE_SHARD)"

# unit writes coverage/agent-cli.out; integration-K writes
# coverage/agent-cli-integration-K.out; integration writes every shard's
# profile; all runs the unit packages and then the integration shards
# (coverage-instrumented runs are too heavy to overlap on a workstation
# without disturbing timing-bound tests).
coverage-agent-cli-shard:
	@set -euo pipefail; \
	mkdir -p "$(COVERAGE_DIR)"; \
	shard="$(AGENT_CLI_COVERAGE_SHARD)"; \
	run_unit() { \
		(cd agent-cli && packages="$(AGENT_CLI_COVERAGE_UNIT_PACKAGES)" && \
			if [ -z "$$packages" ]; then packages="$$(CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) list -tags=nomicrophone ./... | grep -v '/test/integration$$')"; fi && \
			CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" --report-budget --label "agent-cli coverage (unit packages)" -- \
			$(GO) test $$packages $(COVERAGE_COUNT_FLAG) -tags=nomicrophone -timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" -coverpkg=$(AGENT_CLI_COVERPKG) -coverprofile="$(abspath $(COVERAGE_DIR))/agent-cli.out"); \
	}; \
	run_integration() { \
		(cd agent-cli && CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) run $(AGENT_CLI_TEST_RUNNER) --timeout "$(AGENT_CLI_INTEGRATION_TIMEOUT)" --report-budget --label "agent-cli coverage (integration $${1:-all})" -- \
			bash ../scripts/go-test-shards.sh $(AGENT_CLI_INTEGRATION_SHARD_ARGS) $${1:+--shard "$$1"} \
			--build-flag -tags=nomicrophone --build-flag -coverpkg=$(AGENT_CLI_COVERPKG) --cover-prefix "$(abspath $(COVERAGE_DIR))/agent-cli-integration" -- -test.timeout=$(AGENT_CLI_INTEGRATION_TIMEOUT)); \
	}; \
	echo "==> coverage agent-cli shard $$shard"; \
	case "$$shard" in \
		all) run_unit; run_integration ;; \
		unit) run_unit ;; \
		integration) run_integration ;; \
		integration-*) run_integration "$${shard#integration-}" ;; \
		*) echo "AGENT_CLI_COVERAGE_SHARD must be all, unit, integration, or integration-K, got $$shard" >&2; exit 2 ;; \
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

# Service floors include callers in the CLI and embedding suite, so the
# changed scope runs every test package that links a changed package (not only
# the changed packages' own tests) and gates only the floors that closure
# measures exactly: the changed packages and their importers. Profiles go to
# COVERAGE_CHANGED_DIR so a partial run never mixes with full profiles.
coverage-changed: ## Run coverage and the gate for the packages affected by changes since COVERAGE_BASE.
	@set -euo pipefail; \
	dir="$(abspath $(COVERAGE_CHANGED_DIR))"; \
	rm -rf "$$dir"; \
	mkdir -p "$$dir"; \
	(cd tools/coveragegate && GOWORK=off CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) run . --affected --repo ../.. --base "$(COVERAGE_BASE)" --tags nomicrophone \
		$(foreach module,$(MODULES),--module-dir ../../$(module)) --standalone-module-dir ../../tests/embedding) >"$$dir/scope.txt"; \
	if grep -q '^scope full' "$$dir/scope.txt"; then \
		echo "==> coverage-changed: $$(sed -n 's/^scope full //p' "$$dir/scope.txt"); running the full coverage scope"; \
		exec $(MAKE) --no-print-directory coverage COVERAGE_SCOPE=full; \
	fi; \
	sed -n 's/^check //p' "$$dir/scope.txt" >"$$dir/check.txt"; \
	echo "==> coverage-changed since $(COVERAGE_BASE): $$(grep -c '^changed ' "$$dir/scope.txt" || true) changed package(s), $$(grep -c '^test ' "$$dir/scope.txt" || true) test package(s) to run, $$(wc -l <"$$dir/check.txt" | tr -d ' ') floor(s) to check"; \
	sed -n 's/^changed /   changed: /p' "$$dir/scope.txt"; \
	packages_for() { awk -v module="$$1" '$$1 == "test" && $$2 == module { print $$3 }' "$$dir/scope.txt" | tr '\n' ' '; }; \
	jobs=(); \
	for module in $(call scheduled_modules,$(COVERAGE_MODULES)); do \
		packages="$$(packages_for "$$module")"; \
		[ -n "$${packages// /}" ] || continue; \
		if [ "$$module" = agent-cli ]; then \
			unit="$$(printf '%s\n' $$packages | grep -vx '$(AGENT_CLI_INTEGRATION_PACKAGE)' | tr '\n' ' ' || true)"; \
			case " $$packages " in \
				*" $(AGENT_CLI_INTEGRATION_PACKAGE) "*) shard=all; [ -n "$${unit// /}" ] || shard=integration ;; \
				*) shard=unit ;; \
			esac; \
			jobs+=("coverage agent-cli ($$shard)::$(MAKE) --no-print-directory coverage-module COVERAGE_MODULE=agent-cli COVERAGE_DIR='$$dir' AGENT_CLI_COVERAGE_SHARD=$$shard AGENT_CLI_COVERAGE_UNIT_PACKAGES='$$unit'"); \
		else \
			jobs+=("coverage $$module::$(MAKE) --no-print-directory coverage-module COVERAGE_MODULE=$$module COVERAGE_DIR='$$dir' COVERAGE_PACKAGES='$$packages'"); \
		fi; \
	done; \
	if [ "$(COVERAGE_INCLUDE_EMBEDDING)" = "1" ] && [ -n "$$(packages_for tests/embedding | tr -d ' ')" ]; then \
		jobs+=("coverage embedding::$(MAKE) --no-print-directory coverage-module COVERAGE_MODULE=embedding COVERAGE_DIR='$$dir'"); \
	fi; \
	if [ "$${#jobs[@]}" -gt 0 ]; then \
		bash scripts/run-bounded.sh --jobs $(TEST_MODULE_JOBS) -- "$${jobs[@]}"; \
	fi; \
	if [ "$(COVERAGE_RUN_GATE)" = "1" ]; then \
		echo "==> coverage gate (changed scope)"; \
		profiles=(); \
		for profile in "$$dir"/*.out; do [ -e "$$profile" ] && profiles+=("$$profile"); done; \
		(cd tools/coveragegate && GOWORK=off CGO_ENABLED=$(BUILD_CGO_ENABLED) $(GO) run . --manifest "$(abspath $(COVERAGE_MANIFEST_DIR))" --select "$$dir/check.txt" $${profiles[@]+"$${profiles[@]}"}); \
	fi

wire-check: ## Regenerate the pinned Wire graph and reject generated-code drift.
	@python3 -B scripts/check-wire.py --go "$(GO)" $(MODULES)

check-ci-test-partition: ## Verify each Linux package corpus has exactly one CI owner.
	@python3 -B scripts/check-ci-test-partition.py .github/workflows/ci.yml

verify-standalone-checkout: ## Prove agent-cli resolves and builds from a standalone checkout (GOWORK=off).
	@GO="$(GO)" scripts/verify-standalone-checkout.sh

# PREPUSH_SCOPE=changed (default) tests and gates coverage only for packages
# affected by changes since COVERAGE_BASE; PREPUSH_SCOPE=full runs every
# package like CI. PREPUSH_JOBS bounds how many independent phases of a stage
# run at once.
PREPUSH_SCOPE ?= changed
PREPUSH_JOBS ?= 4

prepush: ## Run the fail-fast, timed local pre-push gate (PREPUSH_SCOPE=changed|full, PREPUSH_JOBS=N).
	@GO="$(GO)" PREPUSH_MAKE="$(PREPUSH_MAKE)" PREPUSH_SCOPE="$(PREPUSH_SCOPE)" PREPUSH_JOBS="$(PREPUSH_JOBS)" COVERAGE_BASE="$(COVERAGE_BASE)" scripts/prepush.sh

prepush-full: ## Run the pre-push gate over every package (the CI test and coverage scope).
	@$(MAKE) --no-print-directory prepush PREPUSH_SCOPE=full

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
