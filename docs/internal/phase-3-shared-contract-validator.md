# Phase 3 Shared Contract Validator

This validator records the reviewer-facing convergence check for the completed
`phase-3-shared-contract-decision` slice against the current authoritative
checkout.

The decision surface on `origin/main` remains authoritative. This report exists
to show what that branch state does and does not prove today from repository
artifacts alone.

## Scope

- Reviewed decision slice: `phase-3-shared-contract-decision`
- Source branch for validator-only evidence: `phase-3-shared-contract-validator`
  at `386f295`
- Reviewer-facing validator surface: `docs/internal/phase-3-shared-contract-validator.md`
- Supporting decision artifact: `tasks/todo/phase-3-shared-contract-decision.md`
- Explicit exclusions: `docs/internal/checklist.md` and `docs/internal/progress.txt`

## Evidence Inputs

- `tasks/todo/phase-3-shared-contract-decision.md`
- `go-agent-loop/README.md`
- `go-agent-loop/pkg/messages/session.go`
- `go-llm-gateway/pkg/models/doc.go`
- `go-llm-gateway/pkg/models/message.go`
- `go-llm-gateway/pkg/inference/doc.go`
- `go-llm-gateway/pkg/inference/main_inferencer.go`
- `go-llm-gateway/pkg/inference/session_inferencer.go`
- `go-llm-gateway/README.md`
- `go-llm-gateway/docs/development.md`
- `go-llm-gateway/pkg/gateway/session_gateway.go`
- `docs/architecture/dependencies.md`
- `docs/architecture/contract-gap-audit.md`

## Final Landing Validation

- `branchComparisonCommand`:
  `git diff --name-only origin/main...HEAD`
- `branchComparisonResult`:
  `docs/internal/phase-3-shared-contract-validator.md`
- `plannerOwnedFilesCheck`:
  `git diff --name-only origin/main...HEAD -- docs/internal/checklist.md docs/internal/progress.txt`
- `plannerOwnedFilesResult`: no output; this landing branch does not change
  either planner-owned file relative to `origin/main`
- `reverseDependencyInspectionCommand`:
  `cd go-agent-loop && go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./...`
- `reverseDependencyInspectionResult`: no `github.com/portpowered/go-llm-gateway`
  import path was present when inspected for this validator record
- `qualityGateCommands`:
  `make typecheck`, `make lint`, `make test`
- `qualityGateResult`: pass

## Findings

### Authoritative Shared Contract Boundary

- `group`: `P3-CORE-01`
- `subject`: repository-wide ownership claim for the shared message and session
  contract boundary
- `outcome`: `pass`
- `evidence`: `tasks/todo/phase-3-shared-contract-decision.md` names
  `go-agent-loop/pkg/messages` as the authoritative Phase 3 contract boundary.
  That matches the current branch's runtime-facing surfaces:
  `go-agent-loop/README.md` tells consumers to treat `pkg/messages` as the
  shared message, tool, inference, and session contract package, and
  `go-agent-loop/pkg/messages/session.go` declares `SessionInferencer` and
  `Session` in the loop module with comments that explicitly keep provider
  implementations depending on loop-owned contracts rather than the reverse.
  `docs/architecture/dependencies.md` repeats the same module direction by
  describing `go-agent-loop` as the reusable runtime library that owns the
  shared contracts consumed by `go-llm-gateway`.
- `affectedFilesOrSurfaces`: `tasks/todo/phase-3-shared-contract-decision.md`;
  `go-agent-loop/README.md`; `go-agent-loop/pkg/messages/session.go`;
  `docs/architecture/dependencies.md`
- `remainingDrift`: none
- `requiredFollowUp`: none

### Gateway Model Boundary Description

- `group`: `P3-CORE-02`
- `subject`: whether gateway-facing model surfaces describe compatibility
  aliases versus gateway-owned contracts truthfully enough to avoid a second
  shared-contract authority
- `outcome`: `pass`
- `evidence`: `go-llm-gateway/pkg/models/message.go` remains a compatibility
  alias layer over `go-agent-loop/pkg/messages`, and this branch now documents
  that fact consistently in every gateway-facing evidence surface needed for
  review. `go-llm-gateway/pkg/models/doc.go` states that message, tool,
  content-part, and token-usage names in `pkg/models` follow the loop-owned
  contracts and do not define an independent gateway vocabulary. The gateway
  README now distinguishes loop-owned message aliases from gateway-owned session
  config and session-event types, `go-llm-gateway/docs/development.md` mirrors
  the same split for contributors, and `docs/architecture/contract-gap-audit.md`
  records `DOC-01` as resolved for the scoped Phase 3 decision rather than as
  an open ambiguity. Those landed surfaces keep the validator aligned with the
  authoritative decision on this branch instead of relying on branch-only
  reviewer memory.
- `affectedFilesOrSurfaces`: `go-llm-gateway/pkg/models/doc.go`;
  `go-llm-gateway/pkg/models/message.go`; `go-llm-gateway/README.md`;
  `go-llm-gateway/docs/development.md`;
  `docs/architecture/contract-gap-audit.md`
- `remainingDrift`: none
- `requiredFollowUp`: none

### Adapter Composition Boundary

- `group`: `P3-CORE-05`
- `subject`: whether public cross-library composition stays in explicit adapter
  packages instead of hidden loop-core coupling
- `outcome`: `pass`
- `evidence`: the current branch still exposes named adapter entrypoints under
  `go-llm-gateway/pkg/inference`. `GatewayInferencer` and
  `SessionGatewayInferencer` both import `go-agent-loop/pkg/messages` directly
  and include compile-time assertions that they satisfy the loop-owned
  `messages.Inferencer` and `messages.SessionInferencer` interfaces.
  `go-llm-gateway/pkg/gateway/session_gateway.go` also keeps the gateway-layer
  session seam internal and points external callers at the loop-owned
  interface via `SessionGatewayInferencer`. `docs/architecture/dependencies.md`
  and `go-llm-gateway/README.md` describe `pkg/inference` as the intended
  bridge into `go-agent-loop`, so the reviewed branch still has one explicit
  adapter composition story.
- `affectedFilesOrSurfaces`: `go-llm-gateway/pkg/inference/main_inferencer.go`;
  `go-llm-gateway/pkg/inference/session_inferencer.go`;
  `go-llm-gateway/pkg/gateway/session_gateway.go`;
  `go-llm-gateway/README.md`; `docs/architecture/dependencies.md`
- `remainingDrift`: none
- `requiredFollowUp`: none

### Dependency Proof

- `group`: `P3-CORE-06`
- `subject`: reviewer-verifiable proof that `go-agent-loop` does not drift into
  a reverse dependency on `go-llm-gateway`
- `outcome`: `pass`
- `evidence`: the authoritative checkout documents a reviewer-run dependency
  inspection command rather than committing dependency inventory as a quality
  gate test. Reviewers can run
  `cd go-agent-loop && go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./...`
  and confirm that no compiled `go-agent-loop` package depends on
  `github.com/portpowered/go-llm-gateway`. Observable quality-gate coverage for
  the bridge remains in the runtime adapter tests under
  `go-llm-gateway/pkg/inference`.
- `affectedFilesOrSurfaces`: `docs/architecture/dependencies.md`;
  `docs/internal/phase-3-shared-contract-validator.md`;
  `go-llm-gateway/pkg/inference/main_inferencer_test.go`;
  `go-llm-gateway/pkg/inference/session_inferencer_test.go`
- `remainingDrift`: none
- `requiredFollowUp`: none

## Convergence Verdict

- `findingSummary`:
  - `pass`: `P3-CORE-01` authoritative loop-owned shared contract boundary
  - `pass`: `P3-CORE-02` gateway model boundary wording is now explicit across
    package and consumer-facing surfaces
  - `pass`: `P3-CORE-05` public adapter composition remains in explicit
    `pkg/inference` bridges
  - `pass`: `P3-CORE-06` committed reverse-dependency proof is landed and
    reviewer-runnable from the cited `go test` command
- `overallOutcome`: `pass`
- `summary`: the authoritative checkout already presents one clear loop-owned
  contract boundary, one explicit gateway alias-layer description, and one
  explicit adapter composition boundary. It now also includes the committed
  reverse-dependency proof reviewers need for `P3-CORE-06`, so the shared
  contract validator surface is repository-verifiable without relying on
  branch-only evidence for that dependency check. The final landing validation
  now truthfully records that this rebased landing branch differs from
  `origin/main` only by this reviewer-facing validator artifact and confirms
  that `docs/internal/checklist.md` and `docs/internal/progress.txt` stay
  untouched by the landing.
- `broaderPhase3Readiness`: broader Phase 3 independence slices may advance
  from this shared-contract convergence baseline.
