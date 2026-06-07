# Phase 2 Session Fixture Ownership Boundary

This file records the committed expectations for the completed
`phase-2-session-fixture-ownership-boundary` slice that the validator must
confirm or dispute from repository state.

## Scope

The slice establishes the authoritative repository contract for committed shared
`.session.json` replay fixtures before later Phase 2 API hardening work.

## Story Commitments

### phase-2-session-fixture-ownership-boundary-001

- Define one authoritative owner for committed shared `.session.json` replay
  fixtures.
- Publish the canonical repository root for shared committed fixtures.
- State the repository contract in gateway-owned code and documentation so
  reviewers can identify the owner without reconstructing prior branch history.

Primary evidence surfaces:

- `go-llm-gateway/pkg/testing/session_fixture_contract.go`
- `go-llm-gateway/pkg/testing/README.md`
- `go-llm-gateway/pkg/testing/session-fixture-authoring.md`
- `agent-cli/docs/session-record-replay.md`

### phase-2-session-fixture-ownership-boundary-002

- Re-home shared committed replay fixtures under the authoritative gateway-owned
  boundary.
- Keep Agent CLI private `testdata` limited to CLI-private fixtures that are not
  the shared repository contract.
- Provide a stable helper for resolving the shared fixture root from consumers.

Primary evidence surfaces:

- `go-llm-gateway/pkg/testing/session_fixture_paths.go`
- `go-llm-gateway/pkg/testing/testdata/session-fixtures/*.session.json`
- `agent-cli/test/integration/testdata`

### phase-2-session-fixture-ownership-boundary-003

- Update replay and validation consumers to use intentional boundaries instead
  of sibling-module private fixture traversal.
- Prove those consumers can resolve shared fixtures through exported helpers.

Primary evidence surfaces:

- `agent-cli/test/integration/fixture_paths_test.go`
- `agent-cli/test/integration/replay_session_test.go`
- `agent-cli/test/integration/replay_stateless_test.go`
- `agent-cli/test/integration/session_command_test.go`
- `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`

### phase-2-session-fixture-ownership-boundary-004

- Document the allowed fixture authoring and validation workflow.
- Keep repository-level documentation aligned on the same ownership boundary and
  validator invocation path.

Primary evidence surfaces:

- `README.md`
- `go-llm-gateway/README.md`
- `agent-cli/docs/README.md`
- `agent-cli/docs/session-record-replay.md`
- `go-llm-gateway/pkg/testing/README.md`
- `go-llm-gateway/pkg/testing/session-fixture-authoring.md`

### phase-2-session-fixture-ownership-boundary-005

- Prove deterministic replay and fixture validation behavior after the ownership
  cleanup.
- Keep proof focused on observable replay and hygiene contracts rather than
  repository-topology inventory scans.

Primary evidence surfaces:

- `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`
- `go-llm-gateway/pkg/testing/session_replay_test.go`
- root `Makefile` quality targets
