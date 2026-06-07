# Phase 2 Session Fixture Ownership Validator

## Subject Under Review

This validator reviews the completed `phase-2-session-fixture-ownership-boundary`
slice. Run this pass only after that implementation work is complete and the
branch under review is intended to represent the candidate Phase 2 baseline for
session fixture ownership.

The validator does not reopen the implementation scope. It inspects the
delivered repository state as an observable surface and records convergence
evidence for reviewers.

## Scope

This validator produces findings for exactly three areas:

1. Checklist convergence
2. Ownership-boundary architecture drift
3. Replay and session-validation fixture consistency

Every finding in those areas must record:

- `outcome`: `pass`, `fail`, or `uncertain`
- supporting evidence tied to observable repository state
- affected files, fixture roots, or other reviewer-verifiable surfaces
- required follow-up repairs, if any

CI coverage enforcement is not a primary target for this validator. Existing
deterministic replay behavior, fixture validation behavior, or similar checks
may be cited only when they are direct evidence for convergence of the ownership
model.

## Evidence Inputs

The validator should prefer these sources when they exist in the checkout:

- `docs/internal/checklist.md` for the active Phase 2 work inventory
- `tasks/todo/phase-2-session-fixture-ownership-boundary.md` for slice
  commitments
- committed fixture roots, helper APIs, tests, and docs that define ownership or
  consumer boundaries

If one of the expected planning surfaces is missing from the repository state,
record that gap as evidence and classify the impacted finding as `uncertain`
rather than assuming the missing source still exists elsewhere.

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
- `checklist rows / commitments inspected`: the Phase 2 inventory rows that the
  PRD says live in `docs/internal/checklist.md`; the ownership-boundary slice
  commitments that the PRD says live in
  `tasks/todo/phase-2-session-fixture-ownership-boundary.md`
- `affected files / surfaces`: missing `docs/internal/checklist.md`; missing
  `tasks/todo/phase-2-session-fixture-ownership-boundary.md`; `prd.json`;
  `progress.txt`; `docs/internal/phase-2-session-fixture-ownership-validator.md`;
  `go-llm-gateway/pkg/testing/session-fixture-authoring.md`;
  `agent-cli/docs/session-record-replay.md`
- `evidence`: the repository checkout does not contain either authoritative
  planning surface named by the PRD, so the validator cannot inspect the actual
  checklist rows or the actual ownership-boundary task commitments from the
  repository state under review. That missing evidence is itself observable
  repository evidence against convergence because the validator contract depends
  on those files being reviewer-verifiable inputs. The checkout does contain the
  validator report scaffold plus fixture-related docs in
  `go-llm-gateway/pkg/testing/session-fixture-authoring.md` and
  `agent-cli/docs/session-record-replay.md`, which confirms that fixture
  ownership and replay guidance exists, but those surfaces do not map back to
  the specific Phase 2 checklist rows or slice commitments that reviewers were
  instructed to validate. Because the authoritative inventory and slice-plan
  surfaces are absent, no row-level or commitment-level convergence claim can be
  marked `pass`, and no unmet commitment can be marked `fail` with the required
  source-of-truth citation.
- `required repairs`: restore or commit `docs/internal/checklist.md` and
  `tasks/todo/phase-2-session-fixture-ownership-boundary.md` to the branch, or
  replace them with clearly documented successor surfaces that the validator can
  cite directly for row-by-row and commitment-by-commitment convergence review

### Ownership-Boundary Architecture Drift

- `outcome`: `fail`
- `checklist rows / commitments inspected`: the PRD acceptance commitment that
  one authoritative owner for committed shared `.session.json` fixtures now
  exists; the commitment that replay, validation, and other changed consumers
  use intentional helper or repository boundaries rather than sibling-module
  private `testdata` traversal
- `affected files / surfaces`: `go-llm-gateway/pkg/testing/session_fixture_validator.go`;
  `go-llm-gateway/internal/sessionfixturevalidator/command.go`;
  `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`;
  `go-llm-gateway/pkg/testing/testdata/session-fixtures/synthetic-text.session.json`;
  `go-llm-gateway/pkg/providers/openai/testdata/realtime_text.session.json`;
  `agent-cli/test/integration/testdata/*.session.json`;
  `agent-cli/internal/services/session.go`;
  `agent-cli/test/integration/replay_session_test.go`;
  `agent-cli/test/integration/replay_stateless_test.go`;
  `go-llm-gateway/pkg/testing/README.md`;
  `go-llm-gateway/pkg/testing/session-fixture-authoring.md`;
  `agent-cli/docs/session-record-replay.md`
- `evidence`: the repository does show one intentional consumer boundary for
  replay and validation behavior: runtime and test consumers call shared
  `go-llm-gateway/pkg/testing` helpers such as `ValidateSessionCaptureFile`,
  `NewReplayWebSocketDialer`, and `NewSessionReplayer` instead of opening live
  provider connections or reading sibling-module private fixture files directly.
  `agent-cli/internal/services/session.go` routes replay through
  `gwtesting.NewReplayWebSocketDialer`, and
  `agent-cli/test/integration/replay_session_test.go` uses
  `gwtesting.NewSessionReplayer`, which is evidence that behavior-level
  consumers cross the module boundary through exported APIs rather than hidden
  filesystem reach-through.

  The ownership side does not converge to one authoritative committed fixture
  owner, though. Committed `.session.json` fixtures remain split across at least
  three roots: `go-llm-gateway/pkg/testing/testdata/session-fixtures`,
  `go-llm-gateway/pkg/providers/openai/testdata`, and
  `agent-cli/test/integration/testdata`. The authoritative hygiene smoke test in
  `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`
  hard-codes all three roots, including a relative traversal into
  `../../../agent-cli/test/integration/testdata`. That makes the gateway
  validator the de facto policy owner while still depending on cross-module path
  knowledge to find committed fixtures. Reviewers can therefore verify that the
  fixture contract is centralized, but not that the fixture files themselves now
  live under one clear owner or one documented repository boundary.

  The documentation surfaces reinforce that ambiguity. The gateway authoring
  guide tells reviewers to run the validator from `go-llm-gateway` against
  `./pkg/testing`, while `agent-cli/docs/session-record-replay.md` separately
  instructs contributors to commit Agent CLI fixtures under
  `agent-cli/test/integration/testdata` and invoke the gateway validator against
  that external path. Those instructions show intentional cooperation, but they
  also confirm that fixture ownership is still distributed by convention across
  module-local roots instead of converged on one authoritative committed home.
- `required repairs`: define and document one authoritative committed fixture
  owner for shared session captures, or explicitly document a stable ownership
  map with a non-relative discovery boundary; remove the validator's need to
  reach into `agent-cli` through `../../../...` path knowledge; align fixture
  docs so reviewers can identify one source of truth for where shared
  `.session.json` fixtures belong and who owns them

### Replay and Session-Validation Fixture Consistency

- `outcome`: `fail`
- `checklist rows / commitments inspected`: the PRD acceptance commitment that
  replay fixtures and session-validation targets remain consistent with the
  authoritative ownership model after the ownership-boundary slice; the
  commitment that fixture provenance, sanitization expectations, and
  deterministic replay assumptions remain aligned without broadening into
  duplicate CI review
- `affected files / surfaces`: `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`;
  `go-llm-gateway/internal/sessionfixturevalidator/command.go`;
  `go-llm-gateway/pkg/testing/session_fixture_validator.go`;
  `go-llm-gateway/pkg/testing/session-fixture-authoring.md`;
  `go-llm-gateway/pkg/testing/README.md`;
  `go-llm-gateway/pkg/providers/openai/testdata/realtime_text.session.json`;
  `agent-cli/test/integration/testdata/openai_realtime_*.session.json`;
  `agent-cli/test/integration/testdata/session_*.session.json`;
  `agent-cli/docs/session-record-replay.md`;
  `agent-cli/internal/services/session.go`;
  `agent-cli/test/integration/replay_session_test.go`;
  `agent-cli/test/integration/replay_stateless_test.go`;
  `agent-cli/test/integration/session_command_test.go`
- `evidence`: the committed session-fixture validator and replay helpers now
  provide matching executable evidence for the currently checked-in
  `.session.json` files. The smoke-check roots in
  `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`
  cover every replayable committed `.session.json` fixture in the repository
  except the validator's own intentionally invalid negative fixtures. Those same
  tests also prove that each committed session fixture still loads through the
  replay surface implied by its payload shape: normalized `stream_message`
  captures load through `gwtesting.NewSessionReplayer`, and raw provider
  `websocket_message` captures load through
  `gwtesting.NewReplayWebSocketDialer`. The checked-in fixture data is therefore
  internally consistent with the current replay and hygiene validator
  implementation, and the targeted replay tests in `agent-cli` continue to use
  the same committed fixture files that the gateway validator scans.

  The consistency finding still fails because the repository does not expose one
  coherent documented fixture-root contract. `go-llm-gateway/pkg/testing/README.md`
  and `go-llm-gateway/pkg/testing/session-fixture-authoring.md` both instruct
  reviewers to validate fixtures from `./pkg/testing`, while
  `agent-cli/docs/session-record-replay.md` separately declares
  `agent-cli/test/integration/testdata` as the committed Agent CLI fixture home
  and tells contributors to invoke the gateway validator against that external
  path. The validator smoke test additionally treats
  `go-llm-gateway/pkg/providers/openai/testdata/realtime_text.session.json` as a
  committed session fixture root, but that root is not named in either fixture
  authoring guide. Reviewers can verify that replay inputs and validation
  targets currently agree on payload hygiene and replayability, but they cannot
  derive one authoritative documented root set for those same committed session
  fixtures.
- `required repairs`: publish one reviewer-facing ownership map or one
  authoritative committed fixture root for session captures; update the gateway
  fixture authoring guide and Agent CLI replay doc so they point to the same
  validator target set, including the OpenAI provider-session fixture root if it
  remains committed; keep the executable validator root list aligned with that
  published contract

## Dead-End and Stale Documentation References

- `dead-end planning references`: `prd.json` directs reviewers to
  `docs/internal/checklist.md` and
  `tasks/todo/phase-2-session-fixture-ownership-boundary.md`, but neither file
  exists in this checkout. Those references are dead ends for checklist
  convergence review because the validator cannot cite row-level or
  commitment-level source material from the repository under review.
- `contradictory fixture-root guidance`: `go-llm-gateway/pkg/testing/README.md`
  and `go-llm-gateway/pkg/testing/session-fixture-authoring.md` present
  `./pkg/testing` as the validator target surface, while
  `agent-cli/docs/session-record-replay.md` presents
  `agent-cli/test/integration/testdata` as a committed fixture home and tells
  contributors to validate that external path from `go-llm-gateway`. Reviewers
  therefore encounter two different documentation answers for the same
  ownership question.
- `undocumented committed root`: the executable validator evidence in
  `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`
  treats `go-llm-gateway/pkg/providers/openai/testdata` as a committed session
  fixture root, but neither contributor-facing fixture guide names that root.
  That leaves a hidden contract between the validator implementation and the
  repository layout.

## Required Repairs Before Next Phase 2 Slice

| Repair | Triggering evidence | Affected files / surfaces | Blocks |
|--------|---------------------|---------------------------|--------|
| Restore the authoritative checklist inventory and ownership-boundary slice plan, or publish explicit successor surfaces. | Checklist convergence is `uncertain` because the PRD-named planning inputs are absent from the repository state. | Missing `docs/internal/checklist.md`; missing `tasks/todo/phase-2-session-fixture-ownership-boundary.md`; `prd.json` | Checklist convergence acceptance criteria and reviewer-verifiable row mapping |
| Define one authoritative committed fixture owner, or publish one stable ownership map that removes relative cross-module discovery. | Ownership-boundary architecture is `fail` because committed fixtures remain split across gateway, provider, and Agent CLI roots and the validator still relies on `../../../agent-cli/...` path knowledge. | `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`; `go-llm-gateway/pkg/testing/session_fixture_validator.go`; `agent-cli/test/integration/testdata`; `go-llm-gateway/pkg/providers/openai/testdata` | Ownership-boundary acceptance criteria and the next Phase 2 slice baseline |
| Align every fixture-facing doc and validator target list on the same committed root set, including the provider-session fixture root if it remains committed. | Replay/validation consistency is `fail` because docs and executable discovery disagree about which repository roots define the committed session-fixture contract. | `go-llm-gateway/pkg/testing/README.md`; `go-llm-gateway/pkg/testing/session-fixture-authoring.md`; `agent-cli/docs/session-record-replay.md`; `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go` | Replay/session-validation consistency acceptance criteria and reviewer understanding |

## Convergence Verdict

- `overall outcome`: `uncertain`
- `summary`: the validator now records one `uncertain` finding and two `fail`
  findings. Checklist
  convergence remains `uncertain` because the required planning inputs named by
  the PRD are missing from the repository state. Ownership-boundary architecture
  drift is `fail`: shared replay and validation behavior does route through
  intentional `go-llm-gateway/pkg/testing` APIs, but committed `.session.json`
  fixtures still live under multiple roots and the gateway validator still uses
  relative cross-module path knowledge to discover `agent-cli` fixtures.
  Replay/session-validation consistency is also `fail`: the committed fixture
  data remains replayable and hygiene-validated, but contributor-facing docs and
  validator target roots still disagree about which repository surfaces define
  the committed session-fixture contract. The dead-end planning references,
  contradictory fixture-root guidance, and undocumented provider fixture root
  are now called out explicitly so reviewers can verify every remaining repair
  without reconstructing prior batch history.
- `required repairs before next Phase 2 slice`: restore or replace the missing
  authoritative planning surfaces for checklist validation; converge on one
  authoritative committed fixture owner or explicitly documented ownership map
  that removes hidden relative-path coupling; align the gateway fixture
  authoring guide, Agent CLI replay doc, and validator root list on one
  reviewer-verifiable committed session-fixture contract
