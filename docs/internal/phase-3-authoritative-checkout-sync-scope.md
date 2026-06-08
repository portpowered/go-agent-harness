# Phase 3 Authoritative Checkout Sync Scope

## Authority

This reconciliation slice uses `origin/main` merge commit `cf9efa1` as the
authoritative Phase 3 shared-contract decision source.

At iteration start:

- local `main`: `bd1c714`
- current branch `phase-3-authoritative-checkout-sync`: `bd1c714`
- `origin/main`: `cf9efa1`

The authoritative comparison for this slice is therefore:

- `git diff --name-status main..origin/main`

## In-Scope Decision Surface

This iteration records the exact landed Phase 3 surfaces that later sync
stories may restore from `origin/main` without inventing a second contract
decision:

- `docs/architecture/dependencies.md`
- `docs/architecture/contract-gap-audit.md`
- `go-agent-loop/pkg/messages/doc.go`
- `go-llm-gateway/pkg/models/doc.go`
- `go-llm-gateway/pkg/models/message.go`
- `go-llm-gateway/pkg/inference/doc.go`
- `go-agent-loop/test/architecture/dependency_direction_test.go`
- `go-agent-loop/test/architecture/reviewer_evidence_test.go`

These files are the reviewer-facing architecture docs, package comments, and
dependency-proof tests cited by the PRD acceptance criteria and by the landed
Phase 3 decision wording already visible on `origin/main`.

## Explicit Exclusions

The authoritative `main..origin/main` diff contains additional files outside the
Phase 3 shared-contract decision surface for this work item. Those files are out
of scope for this reconciliation unless a later story reaches the explicit
mergeability-only exception described in the PRD.

The scope explicitly excludes planner-owned files:

- `docs/internal/checklist.md`
- `docs/internal/progress.txt`

It also excludes unrelated runtime, provider, CLI, README, and validator
follow-up files that appear elsewhere in the remote diff but are not part of the
shared-contract decision surface named above.

## Guardrails

- Do not introduce a second shared-contract package or alternate shared-message
  owner.
- Treat `go-agent-loop/pkg/messages` as the one authoritative shared-contract
  boundary for this Phase 3 slice.
- Restore only the landed `origin/main` wording and proof surfaces needed for
  reviewers to inspect that boundary locally.
- Preserve unrelated local state unless a later story needs a minimal
  mergeability repair on the current reviewed head.

## Validation Snapshot

Because the repository root is a `go.work` workspace rather than a module, root
`go test ./...` is not a valid typecheck command here. Compile and package
validation for this scope was verified with the workspace-aware command:

- `go test ./agent-cli/... ./go-agent-loop/... ./go-llm-gateway/...`
