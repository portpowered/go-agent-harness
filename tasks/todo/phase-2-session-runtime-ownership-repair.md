# Phase 2 Session Runtime Ownership Repair

This file records the committed expectations for the completed
`phase-2-session-runtime-ownership-repair` slice that the validator must
confirm or dispute from repository state.

## Scope

This slice closes the remaining Phase 2 constructor-ownership gap for
session-mode runtime behavior in the scoped Grok and OpenAI record/replay
paths, then records reviewer-facing evidence for the delivered ownership model.

## Story Commitments

### phase-2-session-runtime-ownership-repair-001

- Define one explicit CLI-owned session runtime seam before provider-specific
  record, replay, or live session construction begins.
- Keep session-mode config resolution, dialer ownership, and provider-specific
  runtime selection reviewable at that seam.

Primary evidence surfaces:

- `agent-cli/internal/services/session.go`
- `agent-cli/internal/services/session_runtime.go`
- `agent-cli/internal/services/session_test.go`

### phase-2-session-runtime-ownership-repair-002

- Route scoped Grok and OpenAI record behavior through explicitly injected
  runtime dependencies.
- Fail clearly when provider session runtime wiring is missing an owned dialer
  instead of creating a hidden live default inside constructors.

Primary evidence surfaces:

- `agent-cli/internal/services/session_runtime.go`
- `agent-cli/internal/services/session_test.go`
- `go-llm-gateway/pkg/providers/grok/provider.go`
- `go-llm-gateway/pkg/providers/grok/provider_test.go`
- `go-llm-gateway/pkg/providers/openai/session.go`
- `go-llm-gateway/pkg/providers/openai/session_test.go`

### phase-2-session-runtime-ownership-repair-003

- Preserve one explicit cancellation contract for replay and record relay
  writes.
- Bind replay and recorder relay lifetime to the owned caller or session
  context rather than `context.Background()`.

Primary evidence surfaces:

- `go-llm-gateway/pkg/testing/session_record.go`
- `go-llm-gateway/pkg/testing/session_replay.go`
- `go-llm-gateway/pkg/testing/session_inferencer.go`
- `go-llm-gateway/pkg/testing/session_record_test.go`
- `go-llm-gateway/pkg/testing/session_replay_test.go`
- `agent-cli/internal/services/session.go`

### phase-2-session-runtime-ownership-repair-004

- Prove the repaired runtime seam with deterministic tests that do not require
  live credentials or external network access.
- Keep proof focused on observable session runtime behavior and cancellation at
  the repaired seam.

Primary evidence surfaces:

- `agent-cli/internal/services/session_test.go`
- `go-llm-gateway/pkg/testing/session_record_test.go`
- `go-llm-gateway/pkg/testing/session_replay_test.go`
- root `Makefile` quality targets

### phase-2-session-runtime-ownership-repair-005

- Refresh reviewer-facing docs, architecture audit evidence, and checklist
  references to match the delivered ownership model.
- Record which findings are resolved or narrowed by this slice and how the
  work satisfies `P2-SRO-04`, `P2-GATE-01`, and advances the broader
  constructor-ownership row `P2-COB-04`.

Primary evidence surfaces:

- `docs/internal/checklist.md`
- `docs/internal/phase-2-session-runtime-ownership-validator.md`
- `docs/architecture/contract-gap-audit.md`
- `docs/architecture/dependencies.md`
- `agent-cli/docs/session-record-replay.md`
- `go-llm-gateway/pkg/testing/README.md`
