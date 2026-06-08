# Internal Checklist

## Phase 3 - Shared Contract Decision

This checklist is the authoritative Phase 3 inventory for the shared-contract
decision slice. Reviewers should cite these item IDs directly when validating
that the Phase 3 boundary choice is complete and evidenced.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P3-CORE-01` | Authoritative shared contract boundary | The repository names `go-agent-loop/pkg/messages` as the authoritative shared contract boundary for cross-library message, stream, tool, token-usage, inference, and session contracts, with reviewer-citable rationale for keeping that boundary in the loop module during this phase. | `go-agent-loop/pkg/messages/doc.go`, `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md`, `go-llm-gateway/pkg/inference/main_inferencer_test.go` |
| `P3-CORE-02` | Gateway compatibility layer and bridge evidence | Gateway-facing shared-message surfaces are documented as compatibility aliases over loop-owned contracts, public adapter packages are described as bridges into that boundary, and runtime adapter proofs provide the primary observable evidence for bridge behavior. Dependency inventories may be used as reviewer inspection commands, but they are not quality-gate tests for this row. | `go-llm-gateway/pkg/models/doc.go`, `go-llm-gateway/pkg/models/message.go`, `go-llm-gateway/pkg/inference/doc.go`, `go-llm-gateway/pkg/gateway/session_gateway.go`, `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md`, `go-llm-gateway/pkg/inference/main_inferencer_test.go`, `go-llm-gateway/pkg/inference/session_inferencer_test.go` |

## Phase 1 - Authoritative Checkout Baseline

This checklist is the Phase 1 inventory for the authoritative checkout baseline.
The convergence validator must cite these item IDs directly when it records
`pass`, `fail`, or `uncertain` evidence.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P1-CHK-01` | Checklist source | The repository contains an authoritative Phase 1 checklist that reviewers can cite directly during convergence validation. | `docs/internal/checklist.md` |
| `P1-CHK-02` | Checklist convergence | The convergence report maps the repaired Phase 1 baseline to the relevant checklist items and required outcomes with explicit evidence and affected surfaces. | `docs/internal/phase-1-authoritative-checkout-convergence-report.md`, `README.md`, `Makefile`, `.github/workflows/ci.yml`, `go.work`, `docs/architecture/*` |
| `P1-ARCH-01` | Root workspace baseline | The root workspace contract is coherent across the repository root, `go.work`, the root `Makefile`, and the workspace architecture documentation. | `README.md`, `go.work`, `Makefile`, `docs/architecture/workspace.md` |
| `P1-ARCH-02` | CI baseline | GitHub Actions delegates to the same deterministic root validation pipeline contributors run locally. | `.github/workflows/ci.yml`, `Makefile`, `docs/architecture/workspace.md` |
| `P1-ARCH-03` | Dependency and audit alignment | Dependency direction and contract-gap audit documents describe the same Phase 1 module architecture exposed by the repository. | `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md`, module `README.md` files |
| `P1-ARCH-04` | README set coherence | Module and docs index READMEs describe the current repository layout and entrypoint paths without stale directory references. | `README.md`, `agent-cli/README.md`, `agent-cli/docs/README.md`, `go-agent-loop/README.md`, `go-llm-gateway/README.md` |
| `P1-MERGE-01` | Split-brain resolution | The authoritative Phase 1 baseline clearly identifies whether local `main`, `origin/main`, and the prior convergence branch still compete or whether any remaining divergence is only an explicitly documented stale ref. | local `main`, `origin/main`, `phase-1-authoritative-workspace-convergence`, `phase-1-authoritative-checkout-reconciliation` |
| `P1-MERGE-02` | Reviewer readiness | The current head is mergeable, the convergence report records an overall verdict, and any remaining repair work is explicitly scoped from observable evidence. | `docs/internal/phase-1-authoritative-checkout-convergence-report.md`, PR mergeability state, root validation commands |

## Phase 2 - Session Fixture Ownership Boundary

This checklist is the authoritative Phase 2 inventory for the session fixture
ownership-boundary slice. The Phase 2 validator must cite these item IDs
directly when it records `pass`, `fail`, or `uncertain` evidence.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P2-SFO-01` | Ownership contract | The repository names one authoritative owner and canonical root for committed shared `.session.json` replay fixtures. | `go-llm-gateway/pkg/testing/session_fixture_contract.go`, `go-llm-gateway/pkg/testing/README.md`, `go-llm-gateway/pkg/testing/session-fixture-authoring.md` |
| `P2-SFO-02` | Fixture root convergence | Shared committed replay fixtures that form the repository-wide contract live under the gateway-owned canonical root instead of Agent CLI private `testdata`. | `go-llm-gateway/pkg/testing/session_fixture_paths.go`, `go-llm-gateway/pkg/testing/testdata/session-fixtures/*.session.json`, `agent-cli/test/integration/testdata` |
| `P2-SFO-03` | Intentional consumer boundaries | Replay and validation consumers use exported gateway-owned helper APIs or shared fixture path helpers rather than sibling-module filesystem reach-through. | `agent-cli/test/integration/fixture_paths_test.go`, `agent-cli/test/integration/replay_session_test.go`, `agent-cli/test/integration/replay_stateless_test.go`, `agent-cli/test/integration/session_command_test.go`, `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go` |
| `P2-SFO-04` | Authoring guidance | Contributor-facing docs describe the same ownership boundary, fixture classes, and validator workflow without contradictory shared-root guidance. | `README.md`, `go-llm-gateway/README.md`, `agent-cli/docs/README.md`, `agent-cli/docs/session-record-replay.md`, `go-llm-gateway/pkg/testing/README.md`, `go-llm-gateway/pkg/testing/session-fixture-authoring.md` |
| `P2-SFO-05` | Deterministic proof | The repository contains deterministic tests that prove shared fixtures still satisfy replay and hygiene expectations after the ownership move. | `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`, `go-llm-gateway/pkg/testing/session_replay_test.go`, root `Makefile` quality targets |

## Phase 2 - Session Runtime Ownership Repair

This checklist is the authoritative Phase 2 inventory for the session runtime
ownership repair slice. The Phase 2 validator must cite these item IDs
directly when it records `pass`, `fail`, or `uncertain` evidence.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P2-SRO-01` | CLI-owned runtime seam | `agent-cli` owns one explicit composition seam for session-mode provider selection, config resolution, dialer ownership, and provider-specific runtime wiring before session construction begins. | `agent-cli/internal/services/session.go`, `agent-cli/internal/services/session_runtime.go`, `agent-cli/internal/services/session_test.go`, `docs/architecture/dependencies.md` |
| `P2-SRO-02` | Injected provider session runtime | Scoped Grok and OpenAI session record/replay paths consume injected runtime dependencies and fail explicitly when required owned dialers are missing instead of creating hidden live defaults. | `go-llm-gateway/pkg/providers/grok/provider.go`, `go-llm-gateway/pkg/providers/grok/provider_test.go`, `go-llm-gateway/pkg/providers/openai/session.go`, `go-llm-gateway/pkg/providers/openai/session_test.go`, `agent-cli/internal/services/session_test.go` |
| `P2-SRO-03` | Relay cancellation contract | Shared replay and recording helpers preserve caller-owned or session-owned cancellation through the runtime seam rather than switching relay writes onto `context.Background()`. | `go-llm-gateway/pkg/testing/session_record.go`, `go-llm-gateway/pkg/testing/session_replay.go`, `go-llm-gateway/pkg/testing/session_inferencer.go`, `go-llm-gateway/pkg/testing/session_record_test.go`, `go-llm-gateway/pkg/testing/session_replay_test.go` |
| `P2-SRO-04` | Reviewer-visible ownership evidence | Reviewer-facing docs and architecture audit surfaces describe the delivered session runtime ownership model, name the findings resolved or narrowed by this slice, and give enough evidence for follow-up validation without reconstructing planner intent. | `docs/architecture/contract-gap-audit.md`, `docs/architecture/dependencies.md`, `agent-cli/docs/session-record-replay.md`, `go-llm-gateway/pkg/testing/README.md`, `docs/internal/phase-2-session-runtime-ownership-validator.md` |
| `P2-GATE-01` | Deterministic runtime proof and quality gate | Deterministic tests cover the repaired session seam and cancellation contract without live credentials or network access, and the changed workspace surfaces pass the required validation commands. | `agent-cli/internal/services/session_test.go`, `go-llm-gateway/pkg/testing/session_record_test.go`, `go-llm-gateway/pkg/testing/session_replay_test.go`, root `Makefile` quality targets |

## Phase 2 - Constructor Ownership Boundary

This checklist is the authoritative Phase 2 inventory for the constructor
ownership-boundary slice. The Phase 2 validator must cite these item IDs
directly when it records `pass`, `fail`, or `uncertain` evidence.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P2-COB-01` | Checklist source | The repository contains authoritative Phase 2 constructor-ownership checklist rows that reviewers can cite directly during convergence validation. | `docs/internal/checklist.md`, `docs/internal/phase-2-constructor-ownership-validator.md` |
| `P2-COB-02` | Loop constructor ownership | Loop construction makes tool execution ownership explicit instead of silently creating public fallback tool capability. | `go-agent-loop/pkg/agentloop/agent_loop.go`, `go-agent-loop/pkg/agentloop/options.go`, `go-agent-loop/pkg/agentloop/agent_loop_test.go`, `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md` |
| `P2-COB-03` | Stateless provider runtime seam | Live, record, and replay HTTP runtime ownership is composed once in `agent-cli` and injected into provider builders instead of being assembled inside provider-local construction paths. | `agent-cli/internal/agent/provider_runtime.go`, `agent-cli/internal/agent/executor.go`, `agent-cli/internal/agent/provider_factory.go`, `agent-cli/internal/agent/provider_openai.go`, `agent-cli/internal/agent/provider_fal.go`, `agent-cli/internal/agent/provider_runtime_test.go`, `agent-cli/test/integration/provider_runtime_integration_test.go`, `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md` |
| `P2-COB-04` | Record/replay ownership consistency | Record and replay behavior remain aligned with the explicit constructor/runtime ownership seams and do not require hidden live dependency creation or provider-local runtime assembly in either stateless or session mode. | `agent-cli/internal/agent/provider_runtime.go`, `agent-cli/internal/agent/provider_runtime_test.go`, `agent-cli/test/integration/provider_runtime_integration_test.go`, `agent-cli/internal/services/session.go`, `agent-cli/test/integration/session_command_test.go`, `go-llm-gateway/pkg/testing/session_record.go`, `go-llm-gateway/pkg/testing/session_replay.go`, `go-llm-gateway/pkg/testing/session_replay_test.go`, `go-llm-gateway/pkg/testing/session_websocket_dialer_test.go`, `go-llm-gateway/pkg/providers/openai/provider.go`, root `Makefile` quality targets |
| `P2-COB-05` | Reviewer guidance and repair visibility | The convergence report records stale guidance, missing planning inputs, and every repair required before the next Phase 2 API-hardening slice may begin. | `docs/internal/phase-2-constructor-ownership-validator.md`, `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md`, `tasks/todo/phase-2-constructor-ownership-boundaries.md` |

## Phase 3 - Library Independence Proof

This checklist is the authoritative Phase 3 inventory for focused library
independence proof. Reviewers should cite these item IDs directly when they
validate that consumer import/use behavior preserves the shared contract
boundary described in `docs/architecture/dependencies.md`.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P3-CORE-03` | Loop consumer independence | A downstream-style loop consumer can import and exercise `go-agent-loop` with a local inferencer implementation and no dependency on `go-llm-gateway/pkg/providers/...`. | `go-agent-loop/test/functional/consumer_independence_test.go`, `docs/internal/phase-3-library-independence-proof.md` |
| `P3-CORE-04` | Gateway consumer independence | A downstream-style gateway consumer can import and exercise `go-llm-gateway` gateway/provider entrypoints while allowing only `go-agent-loop/pkg/messages` from loop-owned packages. | `go-llm-gateway/test/functional/gateway_independence_test.go`, `docs/internal/phase-3-library-independence-proof.md` |
| `P3-GATE-01` | Independence convergence review | The reviewer-facing evidence names exact deterministic commands, expected dependency-boundary outcomes, allowed shared contract package, forbidden dependency classes, and validation results for the Phase 3 library independence proof. | `docs/internal/phase-3-library-independence-proof.md`, root `Makefile` quality targets |

## Phase 2 - Factory Worktree Hygiene Repair

This checklist is the authoritative Phase 2 inventory for the factory
worktree-hygiene repair. Reviewers should cite these item IDs directly when
they validate the setup-workspace ownership contract and its queue-facing
symptom fixes.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `CTRL-FAC-01` | Root sync ownership | `setup-workspace` no longer owns routine root-checkout `git pull` during setup; the contract names that operation as shared-root maintenance outside the hot path. | `factory/scripts/setup-workspace.py`, `factory/docs/overview.md` |
| `CTRL-FAC-02` | Worktree maintenance ownership | `setup-workspace` no longer owns routine root `git worktree prune` during setup; the contract keeps setup focused on ready-worktree resolution and records the queue symptoms that shared-root mutation previously caused. | `factory/scripts/setup-workspace.py`, `factory/docs/overview.md`, `prd.md` |
| `CTRL-FAC-03` | Planner-owned dirty root tolerance | Routine dirty state in `docs/internal/checklist.md` and `docs/internal/progress.txt` does not block setup or reuse, while other dirty root state still fails with a direct unsafe-state error. | `factory/scripts/setup-workspace.py`, `factory/scripts/tests/test_setup_workspace.py`, `factory/docs/overview.md` |
| `CTRL-FAC-04` | Deterministic proof and queue symptom notes | Reviewers can run committed coverage for concurrent setup and planner-dirty reuse, and the factory docs separate repaired setup failures from any still-manual queue token recovery. | `factory/scripts/tests/test_setup_workspace.py`, `factory/docs/overview.md`, `Makefile` |

## Phase 3 - Gateway Runtime Decoupling

This checklist is the authoritative Phase 3 inventory for the gateway runtime
decoupling slice. Reviewers should cite these item IDs directly when they
validate that `go-llm-gateway` now depends only on the deliberate shared
runtime contract boundary in `go-agent-loop/pkg/messages` for this slice.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P3-CHK-01` | Checklist source | The repository contains authoritative Phase 3 checklist rows that reviewers can cite directly for the gateway-runtime decoupling slice. | `docs/internal/checklist.md`, `prd.json` |
| `P3-CORE-04` | Shared runtime boundary truth | Reviewer-facing docs state that `go-agent-loop/pkg/messages` is the only deliberate shared runtime contract boundary for this slice, while provider-local logging now lives behind the gateway-owned `go-llm-gateway/pkg/logging` seam rather than `go-agent-loop/pkg/logging`. | `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md`, `go-llm-gateway/README.md`, `go-llm-gateway/pkg/logging/logger.go` |
| `P3-DOC-01` | Audit and seam evidence | The dependency audit or equivalent reviewer-tracking surfaces record the logging seam replacement as concrete evidence advancing the Phase 3 gateway-independence repair without claiming broader independence than the code and proof enforce. | `docs/architecture/contract-gap-audit.md`, `docs/architecture/dependencies.md`, `docs/internal/checklist.md` |
| `P3-GATE-01` | Deterministic proof and quality gate | Deterministic validation proves the scoped gateway/provider surfaces still pass without live credentials while reviewer-facing dependency inspection remains documentation evidence outside the quality gate. | `go-llm-gateway/pkg/providers/openai`, `go-llm-gateway/pkg/providers/grok`, `go-llm-gateway/pkg/logging`, changed quality-gate commands |

## Phase 3 - Shared Contract Validator

This checklist is the authoritative Phase 3 inventory for the shared-contract
convergence validator. The validator must cite these item IDs directly when it
records `pass`, `fail`, or `uncertain` evidence.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P3-CORE-01` | Authoritative shared contract boundary | The delivered repository exposes one authoritative shared contract boundary, and package comments, architecture docs, and exported naming identify that same owner without competing claims. | `go-agent-loop/pkg/messages/*.go`, `go-agent-loop/README.md`, `go-llm-gateway/pkg/models/message.go`, `go-llm-gateway/README.md`, `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md`, `docs/internal/phase-3-shared-contract-validator.md` |
| `P3-CORE-02` | Truthful gateway model boundary documentation | `go-llm-gateway/pkg/models` and related docs describe compatibility aliases versus gateway-owned surfaces truthfully and do not overstate independent shared-contract ownership. | `go-llm-gateway/pkg/models/message.go`, `go-llm-gateway/pkg/models/session.go`, `go-llm-gateway/README.md`, `go-llm-gateway/docs/development.md`, `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md`, `docs/internal/phase-3-shared-contract-validator.md` |
| `P3-CORE-05` | Explicit adapter composition boundaries | Cross-library composition remains in explicit adapter packages instead of hidden coupling through core packages or ambiguous package ownership. | `go-llm-gateway/pkg/inference/*.go`, `go-llm-gateway/pkg/gateway/*.go`, `go-agent-loop/pkg/messages/*.go`, `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md`, `docs/internal/phase-3-shared-contract-validator.md` |
| `P3-CORE-06` | Reviewer-verifiable dependency proof | The chosen import or architecture proof is reviewer-verifiable and understandable during review without making dependency inventories quality-gate tests. | `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md`, root validation commands, `docs/internal/phase-3-shared-contract-validator.md` |

## Phase 3 - Library Independence Validator

This checklist is the authoritative Phase 3 inventory for the library
independence convergence pass. Reviewers should cite these item IDs directly
when they validate whether the completed gateway decoupling and
library-independence proof slices support closing the broader package-boundary
realignment gate.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P3-CORE-03` | Loop consumer independence | `go-agent-loop` can be imported and exercised through a reviewer-runnable proof surface without importing `go-llm-gateway/pkg/providers/...` packages, requiring live credentials, or requiring network access. | `go-agent-loop` proof command, `go-agent-loop` proof files, `docs/internal/phase-3-library-independence-validator.md` |
| `P3-CORE-04` | Gateway consumer independence | `go-llm-gateway` can be imported and exercised through a reviewer-runnable proof surface without non-contract `go-agent-loop` runtime packages, while allowing the deliberate shared message contract package. | `go-llm-gateway` proof command, `go-llm-gateway` proof files, `docs/internal/phase-3-library-independence-validator.md` |
| `P3-GATE-01` | Broader boundary realignment gate | The repository contains truthful, deterministic, credential-free, network-free proof guidance and one convergence verdict that states whether the broader Phase 3 package-boundary work may close, must pause for repair, or remains blocked by uncertainty. | `docs/internal/phase-3-library-independence-validator.md`, `docs/internal/checklist.md`, proof commands, reviewer-facing proof documentation |

## Phase 4 - Public API Contract Hardening

This checklist is the authoritative Phase 4 inventory for the public API
contract hardening starter work. The Phase 4 API contract validator must cite
these item IDs directly when it records `pass`, `fail`, or `uncertain`
evidence.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P4-API-01` | Context and cancellation contracts | Public APIs that can block or perform provider work expose caller-controlled context, cancellation, and timeout behavior clearly enough for consumers to own request lifetime. | `go-agent-loop/pkg/agentloop`, `go-agent-loop/pkg/engine`, `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/inference`, provider packages, public docs, tests, examples |
| `P4-API-02` | Typed caller-actionable errors | Public gateway, provider, replay, validation, and cancellation failures preserve typed or structured classifications so callers can branch with `errors.Is`, `errors.As`, or documented fields instead of parsing strings. | `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/testing`, provider packages, `agent-cli/internal/services`, public docs, tests, examples |
| `P4-API-03` | Result contracts and failure signals | Public result values and stream events make success, partial success, terminal failure, replay divergence, cancellation, and provider rejection unambiguous. | `go-agent-loop/pkg/agentloop`, `go-agent-loop/pkg/subsystems`, `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/models`, public docs, tests, examples |
| `P4-API-04` | Provider capability discovery | Consumers can discover provider capabilities through public `go-llm-gateway` APIs without importing `go-agent-loop` runtime internals or concrete provider internals. | `go-llm-gateway/pkg/providers`, `go-llm-gateway/pkg/gateway`, provider docs, examples, tests |
| `P4-API-05` | Stream semantics and error preservation | Streaming and session APIs document and preserve completion, cancellation, replay mismatch, provider-close, and error-classification semantics across provider and gateway boundaries. | `go-agent-loop/pkg/subsystems`, `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/inference`, `go-llm-gateway/pkg/testing`, provider stream adapters, public docs, tests |
| `P4-API-06` | Local unsupported-feature validation | Unsupported provider/request features fail locally before provider execution, with inspectable errors that identify the provider, requested feature or mode, and capability state. | `go-llm-gateway/pkg/providers`, `go-llm-gateway/pkg/gateway`, provider validation tests, public docs, examples |
| `P4-API-07` | Dependency injection and hidden side effects | Public constructors and composition seams keep filesystem, environment, process, network, transport, time, and provider runtime dependencies caller-owned or explicitly injected instead of hidden behind defaults. | `agent-cli/internal/agent`, `agent-cli/internal/services`, `go-agent-loop/pkg/agentloop`, `go-llm-gateway/pkg/providers`, `docs/architecture/dependencies.md`, tests |
| `P4-GATE-01` | Public API hardening gate readiness | The completed Phase 4 starter slices have reviewer-verifiable evidence, docs, examples, and credential-free local commands sufficient for planners to decide whether to repair, reconcile, or queue the next Phase 4 feature batch. | `docs/internal/phase-4-api-contract-validator.md`, `docs/architecture/contract-gap-audit.md`, public package docs, tests, examples, root `Makefile` quality targets |
