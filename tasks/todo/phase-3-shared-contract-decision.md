# Phase 3 Shared Contract Decision

This file records the committed expectations for the completed
`phase-3-shared-contract-decision` slice that the validator must confirm or
dispute from repository state.

## Scope

This slice makes one authoritative shared-contract boundary choice for Phase 3,
reduces gateway-owned wording that could imply a second contract authority,
keeps cross-library composition in explicit adapter packages, and adds
reviewer-verifiable dependency evidence for the chosen direction.

## Story Commitments

### phase-3-shared-contract-decision-001

- Name `go-agent-loop/pkg/messages` as the authoritative shared contract
  boundary for cross-library message, stream, tool, token-usage, inference,
  and session contracts.
- Record reviewer-citable rationale for keeping that boundary in the loop
  module during this Phase 3 slice.

Primary evidence surfaces:

- `go-agent-loop/pkg/messages/doc.go`
- `docs/architecture/dependencies.md`
- `docs/architecture/contract-gap-audit.md`

### phase-3-shared-contract-decision-002

- Reduce gateway shared-message naming to an explicit compatibility layer over
  loop-owned contracts instead of implying an independent gateway-owned shared
  vocabulary.
- Distinguish gateway-owned session and realtime surfaces from loop-owned
  message aliases in consumer-facing docs and package comments.

Primary evidence surfaces:

- `go-llm-gateway/pkg/models/doc.go`
- `go-llm-gateway/pkg/models/message.go`
- `go-llm-gateway/pkg/models/session.go`
- `go-llm-gateway/README.md`
- `go-llm-gateway/docs/development.md`

### phase-3-shared-contract-decision-003

- Keep public cross-library composition in explicit adapter packages that act
  as bridges into the loop-owned contract boundary.
- Document those adapter packages so reviewers can distinguish intended bridges
  from hidden coupling or ambiguous ownership.

Primary evidence surfaces:

- `go-llm-gateway/pkg/inference/doc.go`
- `go-llm-gateway/pkg/inference/main_inferencer.go`
- `go-llm-gateway/pkg/inference/session_inferencer.go`
- `go-llm-gateway/pkg/gateway/session_gateway.go`
- `go-llm-gateway/pkg/providers/session_provider.go`
- `docs/architecture/dependencies.md`

### phase-3-shared-contract-decision-004

- Provide automated, reviewer-verifiable proof that compiled
  `go-agent-loop` packages do not depend on `go-llm-gateway`.
- Keep that proof understandable during review and free of forbidden reverse
  imports.

Primary evidence surfaces:

- `go-agent-loop/test/architecture/dependency_direction_test.go`
- `docs/architecture/dependencies.md`
- `docs/architecture/contract-gap-audit.md`

### phase-3-shared-contract-decision-005

- Record the Phase 3 decision closure in checklist and audit surfaces so
  reviewers can map the shared-contract choice to stable checklist rows and a
  consistent evidence set.
- Keep runtime adapter proofs and reviewer-evidence checks aligned with the
  same cited contract-boundary story.

Primary evidence surfaces:

- `docs/internal/checklist.md`
- `docs/architecture/contract-gap-audit.md`
- `docs/architecture/dependencies.md`
- `go-agent-loop/test/architecture/reviewer_evidence_test.go`
- `go-agent-loop/test/architecture/dependency_direction_test.go`
- `go-llm-gateway/pkg/inference/main_inferencer_test.go`
- `go-llm-gateway/pkg/inference/session_inferencer_test.go`
