# Phase 3 Library Independence Validator

## Subject Under Review

This validator reviews the completed Phase 3 gateway decoupling and
library-independence proof slices as the repository state under review. The
named prerequisite slices are:

- `phase-3-gateway-runtime-decoupling`
- `phase-3-library-independence-proof`

Run this convergence pass only after those slices have been delivered and the
branch under review is intended to represent the candidate Phase 3
package-boundary baseline. The validator judges the delivered repository state
from observable artifacts: committed code, tests, documentation, import
relationships, and exact reviewer-runnable commands.

The validator does not reopen those implementation slices and does not treat
planner intent as evidence.

## Scope

This validator records findings for exactly three required groups:

1. Loop consumer independence for `P3-CORE-03`
2. Gateway consumer independence for `P3-CORE-04`
3. Proof-surface truthfulness and broader gate readiness for `P3-GATE-01`

Validation is limited to observable consumer independence, proof truthfulness,
and Phase 3 gate readiness. It is not a broad cleanup lane, a repository-wide
package inventory enforcement pass, a tutorial-writing pass, or an unrelated
documentation refresh. Dependency evidence is relevant only when it supports or
disproves the checked consumer proof surfaces.

## Evidence Inputs

This convergence pass cites these authoritative repository surfaces:

- `docs/internal/checklist.md`
- the delivered `phase-3-gateway-runtime-decoupling` repository changes
- the delivered `phase-3-library-independence-proof` repository changes
- exact proof commands documented for loop and gateway independence
- committed tests, docs, imports, and generated output that reviewers can rerun
  without credentials or network access

If a prerequisite slice's task document is absent, stale, or contradicted by
observable repository behavior, the validator must record the observable
repository state as authoritative and mark the affected finding `fail` or
`uncertain` when that absence blocks verification.

## Shared Finding Template

Use this shape for every finding group:

### [Finding Group Name]

- `outcome`: `pass` | `fail` | `uncertain`
- `checklist rows inspected`:
- `commands run or cited`:
- `affected files / commands / surfaces`:
- `evidence`:
- `required repairs`:

Every finding group must include an explicit `pass`, `fail`, or `uncertain`
outcome, supporting evidence tied to observable repository state, affected files
or commands, and exact repair work for every non-pass outcome. A `pass` finding
must state that no repairs are required.

## Required Finding Groups

### Loop Consumer Independence

- `checklist rows inspected`: `P3-CORE-03`
- `required question`: can a reviewer import and exercise `go-agent-loop`
  through the delivered proof surface without importing
  `go-llm-gateway/pkg/providers/...`, live credentials, or network access?
- `minimum evidence`: the exact loop proof command, the exercised loop behavior,
  and dependency evidence showing that provider packages are excluded from the
  checked consumer path.
- `non-pass repair shape`: name the failing or uncertain command, offending
  provider dependency or proof gap, affected files, and the exact repair needed
  to make the loop proof runnable and truthful.

### Gateway Consumer Independence

- `checklist rows inspected`: `P3-CORE-04`
- `required question`: can a reviewer import and exercise `go-llm-gateway`
  through the delivered proof surface without non-contract `go-agent-loop`
  runtime packages while allowing the deliberate shared message contract?
- `minimum evidence`: the exact gateway proof command, the exercised gateway
  construction or deterministic inference behavior, the allowed shared contract
  package, and dependency evidence showing that non-contract loop runtime
  packages are excluded from the checked consumer path.
- `non-pass repair shape`: name the failing or uncertain command, offending
  non-contract loop dependency or proof gap, affected files, and the exact
  repair needed to make the gateway proof runnable and truthful.

### Proof-Surface Truthfulness and Gate Readiness

- `checklist rows inspected`: `P3-GATE-01`
- `required question`: do reviewer-facing proof docs cite exact deterministic,
  credential-free, network-free commands and truthfully describe what those
  commands prove, and do the combined findings support closing the broader
  Phase 3 package-boundary realignment gate?
- `minimum evidence`: proof documentation, exact loop and gateway proof
  commands, allowed and forbidden dependency classes, command output or test
  results, and one final verdict of `pass`, `fail`, or `uncertain`.
- `non-pass repair shape`: name every mismatched documentation claim,
  non-runnable command, missing proof, or uncertain gate condition with affected
  files or commands and exact repair work.

## Convergence Verdict Contract

The final validator output must summarize the `P3-CORE-03`, `P3-CORE-04`, and
`P3-GATE-01` findings and state one overall verdict:

- `pass`: broader Phase 3 package-boundary work may close.
- `fail`: broader Phase 3 package-boundary work must pause for the listed
  repairs.
- `uncertain`: broader Phase 3 package-boundary work remains blocked by missing
  or contradictory evidence.

The overall verdict may be `pass` only when all required finding groups pass
from reviewer-runnable repository artifacts.

## Findings

### Loop Consumer Independence

- `outcome`: `pass`
- `checklist rows inspected`: `P3-CORE-03`
- `commands run or cited`:
  - `cd go-agent-loop && go test ./pkg/agentloop ./pkg/participants -run 'TestExecute_(SimpleResponse|WithToolCall|MultiTurn)|TestExecuteStreaming_EndToEndDeltas|TestModelRunner_SimpleInference' -count=1`
  - `cd go-agent-loop && go list -deps ./pkg/agentloop ./pkg/participants ./pkg/subsystems ./pkg/engine ./pkg/messages ./pkg/state | sort | rg 'go-llm-gateway/pkg/providers'`
- `affected files / commands / surfaces`:
  - `go-agent-loop/pkg/agentloop/agent_loop_test.go`
  - `go-agent-loop/pkg/participants/model_runner_test.go`
  - `go-agent-loop/pkg/messages/participant_messages.go`
  - `go-agent-loop/pkg/messages/session.go`
  - `go-agent-loop/go.mod`
  - `cd go-agent-loop && go test ./pkg/agentloop ./pkg/participants -run 'TestExecute_(SimpleResponse|WithToolCall|MultiTurn)|TestExecuteStreaming_EndToEndDeltas|TestModelRunner_SimpleInference' -count=1`
  - `cd go-agent-loop && go list -deps ./pkg/agentloop ./pkg/participants ./pkg/subsystems ./pkg/engine ./pkg/messages ./pkg/state | sort | rg 'go-llm-gateway/pkg/providers'`
- `evidence`:
  - The loop proof command passes and exercises observable loop behavior through
    local test doubles rather than a gateway provider. `TestExecute_SimpleResponse`
    constructs `agentloop.New(WithInferencer(inf))`, runs `Execute`, and asserts
    the assistant text returned by the local `mockInferencer`.
    `TestExecute_WithToolCall` runs a tool-call loop with a local
    `mockToolExecutor` and verifies two inference passes. `TestExecute_MultiTurn`
    verifies multi-turn history carry-forward using a local
    `capturingInferencer`. `TestExecuteStreaming_EndToEndDeltas` verifies
    streaming deltas from a local `chunkInferencer`. `TestModelRunner_SimpleInference`
    verifies participant-level model runner behavior with a local
    `testInferencer`.
  - The checked dependency command exits with no matches for
    `go-llm-gateway/pkg/providers`, which is the expected result. The loop
    consumer path relies on `go-agent-loop/pkg/messages.Inferencer` and
    `go-agent-loop/pkg/messages.SessionInferencer` contracts, not gateway
    provider packages.
  - No live credentials or network access are required by the cited loop proof
    command because all inferencer and tool behavior is supplied by in-process
    test doubles.
  - `P3-CORE-03` is ready to close from the checked loop consumer evidence.
- `required repairs`: none.
