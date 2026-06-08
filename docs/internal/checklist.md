# Internal Checklist

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

## Phase 4 - Typed Errors And Stream Repair

This checklist is the authoritative Phase 4 inventory for the typed error and
stream semantics repair. Reviewers should cite these item IDs directly when
they validate provider, gateway, stream, session, replay, cancellation, and
partial-output classification behavior.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P4-API-02` | Typed errors and stream failure contracts | Representative provider rejection, local validation, direct streaming, session, interaction, cancellation, replay mismatch, and partial-output paths expose public typed errors or documented structured event fields, not string-only policy surfaces. | `go-llm-gateway/pkg/providers/errors.go`, `go-agent-loop/pkg/messages/agent_messages.go`, `go-agent-loop/pkg/messages/interaction_events.go`, `go-llm-gateway/pkg/gateway/interaction_types.go`, `go-llm-gateway/pkg/testing/session_replay.go`, `docs/internal/phase-4-typed-errors-stream-repair-evidence.md` |
| `P4-API-03` | Result, lifecycle, and completion semantics | Cancellation, replay divergence, and partial output remain distinguishable from clean completion, total failure, provider rejection, transport failure, and validation failure. | `go-llm-gateway/pkg/gateway/interaction_gateway_test.go`, `go-llm-gateway/pkg/inference/interaction_bridge_test.go`, `go-llm-gateway/pkg/testing/session_replay_test.go`, `go-llm-gateway/pkg/testing/session_websocket_dialer_test.go`, `docs/internal/phase-4-typed-errors-stream-repair-evidence.md` |
| `P4-API-05` | Public gateway, provider, and session surface alignment | Returned errors and event payloads use the same documented provider taxonomy across gateway, provider, loop message, interaction bridge, session, and replay surfaces. | `go-llm-gateway/README.md`, `go-llm-gateway/pkg/providers/errors_test.go`, `go-llm-gateway/pkg/providers/openai/stream_test.go`, `go-llm-gateway/pkg/providers/gemini/stream_test.go`, `go-llm-gateway/pkg/inference/session_inferencer_test.go`, `docs/internal/phase-4-typed-errors-stream-repair-evidence.md` |
| `P4-GATE-01` | Final repair evidence and quality gate | Reviewer-runnable credential-free commands prove the representative repaired behavior, public docs describe caller usage, remaining stale audit claims are narrowed, and root typecheck, tests, and lint pass. | `docs/internal/phase-4-typed-errors-stream-repair-scope.md`, `docs/internal/phase-4-typed-errors-stream-repair-evidence.md`, `go-llm-gateway/README.md`, root `Makefile` quality targets |
