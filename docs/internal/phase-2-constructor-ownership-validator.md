# Phase 2 Constructor Ownership Validator

## Subject Under Review

This validator reviews the completed
`phase-2-constructor-ownership-boundaries` slice. Run this pass only after that
implementation work is complete and the branch under review is intended to
represent the candidate Phase 2 baseline for constructor ownership.

The validator inspects the delivered repository state as an observable surface.
It does not reopen the implementation scope.

## Scope

This validator records findings for exactly three areas:

1. Checklist convergence
2. Constructor-ownership architecture drift
3. Record/replay runtime consistency

Every finding records:

- `outcome`: `pass`, `fail`, or `uncertain`
- supporting evidence tied to observable repository state
- affected files, runtime seams, or other reviewer-verifiable surfaces
- required follow-up repairs, if any

CI coverage enforcement is not a primary target for this validator. Existing
deterministic record/replay behavior, quality targets, or targeted runtime tests
are cited only where they provide direct convergence evidence for the ownership
model.

## Evidence Inputs

This convergence pass cites the following authoritative repository surfaces:

- `docs/internal/checklist.md`
- `tasks/todo/phase-2-constructor-ownership-boundaries.md` when present as the
  committed slice-plan source
- loop constructor, provider runtime, and record/replay surfaces that expose
  constructor ownership as observable repository behavior

If `tasks/todo/phase-2-constructor-ownership-boundaries.md` is missing from the
reviewed branch, the validator must record that as stale or missing planning
guidance instead of silently substituting an undocumented source.

## Shared Finding Template

Use this shape for every finding group:

### [Finding Group Name]

- `outcome`: `pass` | `fail` | `uncertain`
- `checklist rows / commitments inspected`:
- `affected files / surfaces`:
- `evidence`:
- `required repairs`:

## Findings

### Checklist Convergence

- `outcome`: `uncertain`
- `checklist rows / commitments inspected`: `P2-COB-01`, `P2-COB-02`,
  `P2-COB-03`, `P2-COB-04`, `P2-COB-05`; planned slice commitments for
  `phase-2-constructor-ownership-boundaries`
- `affected files / surfaces`: `docs/internal/checklist.md`;
  `docs/internal/phase-2-constructor-ownership-validator.md`;
  `docs/architecture/dependencies.md`;
  `docs/architecture/contract-gap-audit.md`;
  `go-agent-loop/pkg/agentloop/agent_loop.go`;
  `go-agent-loop/pkg/agentloop/options.go`;
  `go-agent-loop/pkg/agentloop/agent_loop_test.go`;
  `agent-cli/internal/agent/provider_runtime.go`;
  `agent-cli/internal/agent/executor.go`;
  `agent-cli/internal/agent/provider_factory.go`;
  `agent-cli/internal/agent/provider_openai.go`;
  `agent-cli/internal/agent/provider_fal.go`;
  `agent-cli/internal/agent/provider_runtime_test.go`;
  `agent-cli/test/integration/provider_runtime_integration_test.go`;
  `tasks/todo/phase-2-constructor-ownership-boundaries.md` (missing)
- `evidence`: `P2-COB-01` now passes because `docs/internal/checklist.md`
  contains a dedicated constructor-ownership Phase 2 inventory that reviewers
  can cite directly during convergence validation. `P2-COB-02` passes because
  current repository state shows explicit loop-side tool capability ownership:
  `agentloop.New(...)` rejects configured tools unless the caller chooses
  `WithToolExecutor(...)` or `WithToolExecutionDisabled()`, and
  `agent_loop_test.go` covers both the constructor failure and explicit
  no-tools path. `P2-COB-03` passes because stateless live, record, and replay
  HTTP runtime policy is composed once in `agent-cli/internal/agent/
  provider_runtime.go`, injected through `ProviderBuildContext.HTTPClient`, and
  exercised by both unit tests and the provider-runtime integration test rather
  than being rebuilt inside provider constructors. `P2-COB-04` is only
  partially satisfied rather than converged: stateless `ask --record` and
  `ask --replay` behavior is observable through the injected runtime seam, and
  session replay remains deterministic without live credentials, but session
  record paths still silently create live WebSocket dialers when callers do not
  inject one and the shared session recorder/replayer relay paths still write
  through `context.Background()` rather than an owned caller context. Those
  remaining runtime seams mean current repository state does not yet prove that
  every record/replay path follows one explicit ownership model after the
  cleanup. `P2-COB-05` is uncertain because the validator's cited slice-plan
  source, `tasks/todo/phase-2-constructor-ownership-boundaries.md`, is missing
  from the reviewed branch, so the repository does not expose one committed
  constructor-ownership planning surface that can be mapped directly to the
  delivered state. That missing source prevents a full pass for checklist
  convergence even though the implemented code and architecture docs provide
  direct evidence for the main loop-constructor and provider-runtime rows.
- `required repairs`: restore or recreate the missing committed slice-plan file
  `tasks/todo/phase-2-constructor-ownership-boundaries.md` so reviewers can map
  constructor-ownership commitments to current repository state without relying
  on inferred intent; remove silent live dialer fallback and background-context
  relay ownership from the remaining record/replay session paths so
  `P2-COB-04` can converge on one explicit runtime ownership model.

### Constructor-Ownership Architecture Drift

- `outcome`: `fail`
- `checklist rows / commitments inspected`: `P2-COB-02`, `P2-COB-03`;
  constructor-ownership commitments for explicit loop tool capability, one
  intentional provider runtime seam, and removal of hidden live dependency
  creation
- `affected files / surfaces`: `go-agent-loop/pkg/agentloop/agent_loop.go`;
  `go-agent-loop/pkg/agentloop/options.go`;
  `go-agent-loop/pkg/agentloop/agent_loop_test.go`;
  `agent-cli/internal/agent/executor.go`;
  `agent-cli/internal/agent/provider_runtime.go`;
  `agent-cli/internal/agent/provider_factory.go`;
  `agent-cli/internal/agent/provider_openai.go`;
  `agent-cli/internal/agent/provider_fal.go`;
  `agent-cli/internal/agent/provider_runtime_test.go`;
  `agent-cli/test/integration/provider_runtime_integration_test.go`;
  `agent-cli/internal/services/session.go`;
  `go-llm-gateway/pkg/providers/openai/provider.go`;
  `docs/architecture/dependencies.md`;
  `docs/architecture/contract-gap-audit.md`
- `evidence`: Loop constructor ownership now passes the intended contract check.
  `agentloop.New(...)` refuses tool definitions unless the caller supplies
  `WithToolExecutor(...)` or intentionally selects
  `WithToolExecutionDisabled()`, and `agent_loop_test.go` proves both the
  constructor failure and explicit no-tools path. Stateless provider runtime
  ownership also passes the intended seam check: `Executor.BuildLoop(...)`
  computes one `ProviderHTTPRuntime` in `agent-cli/internal/agent/
  provider_runtime.go`, then injects that runtime through
  `ProviderBuildContext.HTTPClient` into the registered provider builders
  instead of letting `provider_openai.go` or `provider_fal.go` choose
  record/replay transport policy internally. Unit coverage in
  `provider_runtime_test.go` and CLI integration coverage in
  `provider_runtime_integration_test.go` show replay works without live
  credentials and record mode flushes through the shared runtime seam.
  The overall architecture-drift verdict still fails because hidden live
  dependency creation remains outside that stateless seam. Session recording in
  `agent-cli/internal/services/session.go` still creates a live default dialer
  with `grok.NewDefaultWebSocketDialer()` when no dialer is injected, for both
  Grok and OpenAI realtime record paths. In addition,
  `go-llm-gateway/pkg/providers/openai/provider.go` still assigns
  `NewDefaultWebSocketDialer()` inside the provider constructor when the caller
  does not inject one. Those paths mean the repository has not yet reached the
  broader "no hidden live dependency creation" state described by the validator
  acceptance target, even though the scoped stateless HTTP runtime seam is now
  explicit and centralized. Reviewer guidance is also partially stale:
  `docs/architecture/contract-gap-audit.md` still cites
  `go-agent-loop/pkg/agentloop/agent_loop.go` as present-tense evidence that
  `agentloop.New` injects `&messages.DefaultToolExecutor{}` even though the
  same audit entry later marks that gap as completed. That contradiction no
  longer matches observable constructor behavior and should not be left for
  reviewers to reconcile manually.
- `required repairs`: keep the stateless HTTP seam as-is, but finish the
  broader constructor-ownership cleanup by moving session-mode live dialer
  selection behind one CLI-owned runtime seam instead of silent defaults in
  `agent-cli/internal/services/session.go` and
  `go-llm-gateway/pkg/providers/openai/provider.go`; rewrite the stale
  DI-01 evidence text in `docs/architecture/contract-gap-audit.md` so the audit
  reflects the current explicit-tool-capability contract rather than the
  pre-fix state.

### Record/Replay Runtime Consistency

Validate whether record and replay behavior still run through the intended
injected runtime dependencies after the ownership cleanup, and whether the
observable runtime and test surfaces remain aligned with the explicit ownership
model without broadening into duplicate CI review.

- `outcome`: `fail`
- `checklist rows / commitments inspected`: `P2-COB-04`; constructor-ownership
  commitments for explicit record/replay runtime ownership and deterministic
  behavior through one reviewer-verifiable seam
- `affected files / surfaces`: `agent-cli/internal/agent/provider_runtime.go`;
  `agent-cli/internal/agent/provider_runtime_test.go`;
  `agent-cli/test/integration/provider_runtime_integration_test.go`;
  `agent-cli/internal/services/session.go`;
  `agent-cli/test/integration/session_command_test.go`;
  `go-llm-gateway/pkg/testing/session_record.go`;
  `go-llm-gateway/pkg/testing/session_replay.go`;
  `go-llm-gateway/pkg/testing/session_replay_test.go`;
  `go-llm-gateway/pkg/testing/session_websocket_dialer_test.go`;
  `go-llm-gateway/pkg/providers/openai/provider.go`;
  `docs/architecture/dependencies.md`;
  `docs/architecture/contract-gap-audit.md`
- `evidence`: Stateless record/replay behavior passes the intended runtime-seam
  check. `agent-cli/internal/agent/provider_runtime.go` assembles live, record,
  and replay HTTP client policy once, `provider_runtime_test.go` proves replay
  swaps in capture-backed transport and record mode owns the recorder instance,
  and `provider_runtime_integration_test.go` proves `ask --replay` works with a
  dummy config and no live credentials while `ask --record` flushes the capture
  from the CLI-owned runtime seam. Session replay also preserves deterministic
  ownership behavior on observable surfaces: `agent-cli/internal/services/
  session.go` routes replay through either `gwtesting.NewReplaySessionInferencer`
  or WebSocket-capture replay helpers, and `session_command_test.go`,
  `session_replay_test.go`, and `session_websocket_dialer_test.go` prove those
  replay flows can render transcripts and detect outbound divergence without
  live provider network access. The overall runtime-consistency verdict still
  fails because session record mode does not yet use one explicit injected live
  runtime seam. Both `runLiveSessionRecord(...)` and
  `runOpenAIRealtimeSessionRecord(...)` in `agent-cli/internal/services/
  session.go` silently fall back to `grok.NewDefaultWebSocketDialer()` when the
  caller does not inject a dialer, and `go-llm-gateway/pkg/providers/openai/
  provider.go` still assigns `NewDefaultWebSocketDialer()` inside the provider
  constructor. Those defaults mean record-mode ownership still depends on hidden
  live dependency creation rather than one CLI-owned composition boundary.
  Runtime consistency is also not fully aligned at the relay layer:
  `go-llm-gateway/pkg/testing/session_record.go` and
  `go-llm-gateway/pkg/testing/session_replay.go` forward replay/record buffer
  traffic with `context.Background()`, so capture and replay relay ownership is
  intentionally decoupled from caller cancellation even though the validator's
  target ownership model is moving toward explicit runtime control. Existing
  tests show deterministic replay behavior remains observable, but repository
  state still exposes two ownership leaks that prevent a full pass.
- `required repairs`: move session-mode live dialer selection behind one
  explicit CLI-owned runtime seam for Grok and OpenAI record paths instead of
  silent defaults in `agent-cli/internal/services/session.go` and
  `go-llm-gateway/pkg/providers/openai/provider.go`; decide and document whether
  session replay/record relay writes are intentionally best-effort after caller
  cancellation, then either preserve that contract explicitly or thread owned
  context through `session_record.go` and `session_replay.go` so runtime
  ownership and cancellation behavior match the intended constructor model.

## Outcome Rules

Use `pass` when the reviewed repository state provides direct evidence that the
inspected checklist rows or ownership commitments are satisfied.

Use `fail` when observable repository state contradicts the intended ownership
model, preserves hidden live dependency creation, or leaves the next Phase 2
slice blocked on a concrete repair.

Use `uncertain` when the validator cannot verify the claim from current
repository state alone, including cases where planning inputs, checklist rows,
or runtime/documentation evidence are missing or contradictory.

## Dead-End and Stale Guidance Handling

The validator must record dead-end docs, stale architecture references, and
contradictory ownership guidance when those issues materially affect reviewer
understanding of loop constructor ownership, provider runtime seams, or
record/replay runtime ownership.

Missing planning inputs are evidence. The validator must not replace a missing
committed slice-plan file with inferred intent from unrelated docs without
stating that the original cited source is absent.

## Final Report Requirements

The final convergence report must:

- conclude with one overall Phase 2 constructor-ownership verdict
- summarize the supporting `pass`, `fail`, and `uncertain` findings
- list every remaining repair item required before the next Phase 2
  API-hardening slice may begin
- remain reviewer-verifiable from current repository state without requiring
  broad source archaeology or reconstruction of prior batch history

## Dead-End and Stale Documentation References

- `planning references`: `tasks/todo/phase-2-constructor-ownership-boundaries.md`
  is still missing from the reviewed branch, so the validator cannot map the
  original slice commitments to current repository state through the committed
  planning surface it cites. That missing file is a live reviewer-facing gap,
  not implied intent.
- `architecture audit guidance`: `docs/architecture/contract-gap-audit.md`
  still contains present-tense DI-01 evidence that says
  `agentloop.New(...)` injects a default tool executor even though the same
  audit entry later marks the gap as completed and the current code now rejects
  configured tools unless the caller chooses `WithToolExecutor(...)` or
  `WithToolExecutionDisabled()`. Reviewers should treat that earlier DI-01
  evidence text as stale.
- `runtime ownership guidance`: the stateless provider-runtime seam is now
  centralized in `agent-cli/internal/agent/provider_runtime.go`, but current
  docs still require reviewers to infer that session-mode record ownership has
  not yet reached the same seam boundary because
  `agent-cli/internal/services/session.go` and
  `go-llm-gateway/pkg/providers/openai/provider.go` retain hidden live dialer
  defaults.

## Required Repairs Before Next Phase 2 Slice

1. Restore or intentionally replace the missing committed slice-plan surface
   `tasks/todo/phase-2-constructor-ownership-boundaries.md` so reviewers can
   map constructor-ownership commitments to delivered repository state without
   relying on inferred intent. This blocks a full pass for `P2-COB-05`.
2. Move session-mode live dialer ownership behind one explicit CLI-owned
   runtime seam for Grok and OpenAI record paths instead of silently creating
   default dialers in `agent-cli/internal/services/session.go` and
   `go-llm-gateway/pkg/providers/openai/provider.go`. This blocks a pass for
   the hidden-live-dependency portions of `P2-COB-03` and `P2-COB-04`.
3. Decide and document the intended cancellation contract for session
   replay/record relays, then either preserve that contract explicitly or
   thread owned caller context through
   `go-llm-gateway/pkg/testing/session_record.go` and
   `go-llm-gateway/pkg/testing/session_replay.go` instead of relaying buffer
   writes through `context.Background()`. This blocks a clean runtime-consistency
   pass for `P2-COB-04`.
4. Rewrite the stale DI-01 evidence text in
   `docs/architecture/contract-gap-audit.md` so the audit reflects the current
   explicit tool-execution ownership contract rather than the pre-fix state.
   This blocks reviewer-ready convergence guidance for `P2-COB-05`.

## Convergence Verdict

- `overall outcome`: `fail`
- `summary`: the reviewed repository now provides direct `pass` evidence for
  authoritative constructor-ownership checklist rows, explicit loop-side tool
  capability ownership, and the centralized stateless provider HTTP runtime
  seam. The branch still does not converge overall because checklist
  convergence remains `uncertain` while the cited slice-plan file is missing,
  constructor-ownership architecture drift remains `fail` due to hidden
  session-mode live dialer creation and stale DI-01 audit guidance, and
  record/replay runtime consistency remains `fail` because session record paths
  and relay cancellation behavior still bypass one explicit owned runtime seam.
- `required repairs before next Phase 2 slice`: restore the missing planning
  surface, remove hidden session-mode live dependency creation, resolve the
  relay cancellation ownership gap, and update the stale audit guidance before
  advancing to the next Phase 2 API-hardening slice.
