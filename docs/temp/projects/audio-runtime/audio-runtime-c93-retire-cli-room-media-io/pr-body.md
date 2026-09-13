## Summary

- Extract the room-media contract and private service implementation into `go-agent-runtime/services/roommedia`.
- Keep the CLI room helpers as thin host adapters and retire 220 physical lines from `session_room_run.go` (1,192 -> 972).
- Add Wire construction, an external `GOWORK=off` consumer, focused causal tests, and bounded executable replay evidence.

## Evidence

- Admission: `project-control.py verify-work --type task --name audio-runtime-c93-retire-cli-room-media-io` -> admitted.
- Retirement verifier: passed; branch, startup/planning/current-main ancestry, source census, owned paths, and public boundary all passed.
- `go test ./services/roommedia/... -count=1` and `-race`: passed.
- External consumer with `GOWORK=off`: passed.
- Focused CLI normal/race regressions and full `agentruntime` suite: passed.
- Pinned lint, staticcheck, vet, coverage registration, and architecture-gate unit tests: passed.
- Focused roommedia coverage: 84.8%.
- Bounded room-media executable and audio-tool replay: passed; no credentials or physical/acoustic claim.

## Ownership note

The candidate is based on `origin/main` at `d4766c3dbbf2c198142047ead4449d58dd47d485`. The only remaining repository gate findings are shared files owned by active C79: `scripts/wire-packages.txt` must register the generated Wire file, and `docs/architecture/architecture-size-baseline.json` needs the reduced baseline/stale-entry cleanup. This task does not edit those shared paths; after that lease is released, apply the minimal registry/baseline update on this same task, rerun the two gates, and submit to SCRIPT CI.

## Fresh executor recheck

At head `8c17ddfbddfaf82fdd9d767bef5e642cd9660f8e`, admission and the
retirement verifier still pass (`1,192 -> 972` physical lines, exactly `220`
retired). Roommedia normal and race suites pass at `-count=3` (`30` tests in
four packages each), the separate `GOWORK=off` consumer passes at `-count=3`,
and the focused CLI normal/race suites pass (`156`/`66` tests). The accumulated
`COUNT=1 scripts/test-session-ci-regressions.sh all` matrix passes in normal,
coverage, and race modes, including the expected replay and multi-turn
negative controls. No C93 review findings exist in the canonical board or PR.

The current shared-gate failures remain unchanged and ownership-scoped:
`make wire-check` reports only the unregistered
`go-agent-runtime/services/roommedia/wire/wire_gen.go`; `make
architecture-size-check` reports that generated-file finding plus nine stale or
downward `session_room_run.go` entries in the C79-owned architecture baseline.
The C79 lease is still active, so no shared-file mutation or SCRIPT CI
submission is authorized yet.

## Coverage-floor repair checkpoint

- The changed-coverage run exposed one owned floor deficit in
  `go-agent-runtime/services/roommedia/transports`: 79.10% measured versus the
  existing 80.00% minimum. Added `adapter_test.go` covers the public transport
  seams and failure/identity behavior without changing the contract or floor.
- Post-repair evidence is green: transport normal/race tests pass at count 3,
  focused roommedia normal/race suites pass, transport coverage is 97.8%,
  coverage registration passes all 180 workspace packages, and pinned lint,
  staticcheck, vet, and diff checks pass. The architecture gate is back to the
  exact 10 pre-existing C93 findings: one generated Wire registration, one
  downward retirement entry, and eight stale owned-symbol entries in C79's
  shared baseline.
- No C93 review finding exists. After C79's guarded merge and the separate
  go-audio clock-coverage disposition, fetch/merge current `origin/main`, apply
  only the demonstrated shared C93 registration/baseline changes, rerun bounded
  gates, push this same PR, and return `ACCEPTED` to SCRIPT CI without polling.
