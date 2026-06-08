# Phase 2 Session Runtime Ownership Validator

## Subject Under Review

This validator reviews the completed
`phase-2-session-runtime-ownership-repair` slice. Run this pass only after
that implementation work is complete and the branch under review is intended to
represent the candidate Phase 2 baseline for session runtime ownership.

The validator inspects the delivered repository state as an observable surface.
It does not reopen the implementation scope.

## Scope

This validator records findings for exactly three areas:

1. Checklist convergence
2. Session runtime ownership boundaries
3. Deterministic proof and reviewer-surface alignment

Every finding records:

- `outcome`: `pass`, `fail`, or `uncertain`
- supporting evidence tied to observable repository state
- affected files or other reviewer-verifiable surfaces
- required follow-up repairs, if any

CI enforcement is cited only where it provides direct evidence for the repaired
runtime seam and its deterministic proof.

## Evidence Inputs

This convergence pass cites the following authoritative repository surfaces:

- `docs/internal/checklist.md`
- `tasks/todo/phase-2-session-runtime-ownership-repair.md`
- the code, tests, and docs that expose session runtime ownership, cancellation
  behavior, and reviewer-facing evidence

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

- `outcome`: `pass`
- `checklist rows / commitments inspected`: `P2-SRO-01`, `P2-SRO-02`,
  `P2-SRO-03`, `P2-SRO-04`, `P2-GATE-01`; story commitments
  `phase-2-session-runtime-ownership-repair-001` through
  `phase-2-session-runtime-ownership-repair-005`
- `affected files / surfaces`: `docs/internal/checklist.md`;
  `tasks/todo/phase-2-session-runtime-ownership-repair.md`;
  `agent-cli/internal/services/session.go`;
  `agent-cli/internal/services/session_runtime.go`;
  `agent-cli/internal/services/session_test.go`;
  `go-llm-gateway/pkg/providers/grok/provider.go`;
  `go-llm-gateway/pkg/providers/openai/session.go`;
  `go-llm-gateway/pkg/testing/session_record.go`;
  `go-llm-gateway/pkg/testing/session_replay.go`;
  `docs/architecture/contract-gap-audit.md`
- `evidence`: the repository now contains both authoritative planning inputs
  required by this validator: a Phase 2 checklist section under
  `docs/internal/checklist.md` and a committed slice summary under
  `tasks/todo/phase-2-session-runtime-ownership-repair.md`. Those surfaces map
  directly to the delivered repository state. `P2-SRO-01` passes because the
  CLI session flow now plans one session runtime through
  `agent-cli/internal/services/session_runtime.go` before provider-specific
  session construction begins. `P2-SRO-02` passes because the scoped Grok and
  OpenAI session providers reject missing owned dialers rather than creating
  hidden live defaults. `P2-SRO-03` passes because shared replay and recorder
  relay writes now bind to an explicit lifecycle context and stop when that
  owned context is cancelled. `P2-SRO-04` passes because the architecture docs,
  session replay/record docs, and this validator record the delivered ownership
  contract and the findings resolved or narrowed by this slice. `P2-GATE-01`
  passes because deterministic tests cover runtime planning, injected dialer
  behavior, and relay cancellation without live provider credentials or network
  access.
- `required repairs`: none

### Session Runtime Ownership Boundaries

- `outcome`: `pass`
- `checklist rows / commitments inspected`: `P2-SRO-01`, `P2-SRO-02`,
  `P2-SRO-03`; story commitments
  `phase-2-session-runtime-ownership-repair-001`,
  `phase-2-session-runtime-ownership-repair-002`, and
  `phase-2-session-runtime-ownership-repair-003`
- `affected files / surfaces`: `agent-cli/internal/services/session.go`;
  `agent-cli/internal/services/session_runtime.go`;
  `agent-cli/internal/services/session_test.go`;
  `go-llm-gateway/pkg/providers/grok/provider.go`;
  `go-llm-gateway/pkg/providers/grok/provider_test.go`;
  `go-llm-gateway/pkg/providers/openai/session.go`;
  `go-llm-gateway/pkg/providers/openai/session_test.go`;
  `go-llm-gateway/pkg/testing/session_record.go`;
  `go-llm-gateway/pkg/testing/session_replay.go`;
  `go-llm-gateway/pkg/testing/session_inferencer.go`;
  `go-llm-gateway/pkg/testing/session_record_test.go`;
  `go-llm-gateway/pkg/testing/session_replay_test.go`
- `evidence`: the merged repository now exposes one explicit CLI-owned session
  runtime seam for the scoped providers. `RunSession` enters that seam through
  planner-driven runtime selection instead of constructing provider-specific
  defaults inline. The seam owns config resolution, live versus replay dialer
  choice, and provider-specific inferencer wiring before session construction.
  Grok and OpenAI session providers now require an injected dialer at the
  provider session boundary, which prevents hidden live-dialer creation in both
  CLI service flow and provider constructors. The relay wrappers now preserve
  cancellation semantics by threading the owned lifecycle context into replay
  and recorder write paths instead of switching onto `context.Background()`.
- `required repairs`: none

### Deterministic Proof and Reviewer-Surface Alignment

- `outcome`: `pass`
- `checklist rows / commitments inspected`: `P2-SRO-04`, `P2-GATE-01`; story
  commitments `phase-2-session-runtime-ownership-repair-004` and
  `phase-2-session-runtime-ownership-repair-005`
- `affected files / surfaces`: `agent-cli/internal/services/session_test.go`;
  `go-llm-gateway/pkg/testing/session_record_test.go`;
  `go-llm-gateway/pkg/testing/session_replay_test.go`;
  `agent-cli/docs/session-record-replay.md`;
  `go-llm-gateway/pkg/testing/README.md`;
  `docs/architecture/contract-gap-audit.md`;
  `docs/architecture/dependencies.md`;
  root `Makefile`
- `evidence`: deterministic proof now covers the repaired runtime seam and the
  chosen cancellation contract directly enough for reviewers to distinguish
  owned cancellation from best-effort background draining. The session docs and
  gateway testing docs both describe the same runtime ownership model: the CLI
  owns config loading, dialer selection, and provider runtime injection, while
  the shared testing helpers preserve the caller-owned lifecycle context for
  replay and record relay writes. The architecture audit now records that
  `HC-03`, `DI-04`, and the remaining session-helper portion of `CTX-02` are
  resolved or materially narrowed by this slice, which is the concrete evidence
  reviewers need to assess `P2-COB-04` and `P2-GATE-01` without reconstructing
  planner notes.
- `required repairs`: none

## Dead-End and Stale Documentation References

- `planning references`: no dead-end planning references remain for this slice.
  The validator cites both `docs/internal/checklist.md` and
  `tasks/todo/phase-2-session-runtime-ownership-repair.md` directly.
- `stale ownership claims`: no reviewed surface should continue to claim that
  the scoped session runtime paths still rely on hidden live dialer creation or
  `context.Background()` relay writes. Reviewers should treat any reappearance
  of those claims as stale evidence that must be updated or disproved from code.

## Required Repairs Before Next Phase 2 Slice

No blocking repairs remain for the scoped session runtime ownership slice. The
current repository state gives reviewers one explicit runtime seam, explicit
missing-dialer failure contracts for scoped providers, one shared cancellation
contract for replay and recorder relay writes, and deterministic proof for the
repaired behavior.

## Convergence Verdict

- `overall outcome`: `pass`
- `summary`: checklist convergence, session runtime ownership boundaries, and
  deterministic proof plus reviewer-surface alignment all pass against the
  current repository state. The branch now contains the authoritative Phase 2
  checklist rows and committed slice summary needed for citation, the scoped
  Grok and OpenAI session runtime paths cross one CLI-owned composition seam,
  provider session constructors reject missing owned dialers, replay and record
  relay writes honor the owned lifecycle context, and the architecture/docs
  surfaces record how this slice advances `P2-COB-04` and `P2-GATE-01`.
- `required repairs before next Phase 2 slice`: none
