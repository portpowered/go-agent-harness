# Recut spec — s2s-v6d-error-malformed-response (#166)

Date: 2026-08-25. Evidence: `s2s-vertical-pr-disposition-triage-evidence.md` §1–§3,
table row in §9 of `s2s-program-status-2026-08-17.md`.

## Disposition

| axis | verdict |
|------|---------|
| Outcome already on main? | **No.** The v6d scenario implementation (`go-agent-loop/pkg/probe/scenario_error_malformed_response.go`) and both v6d session fixtures are ABSENT_ON_MAIN; probe-side files DIVERGENT (evidence §2). Partial-overlap nuance: main carries the *v6a auth-error* probe scenario and the generic `session_failure_malformed_frame.session.json` fixture, but no malformed-response vertical; v6d remains unlanded. |
| Conflict class | **Textual/mechanical.** Two content hunks: (1) `probe.go` — main's `deriveToolResultObservation` (~50 lines from #170/#164) inserted where the PR adds v6d registration; different content, same site, union resolves. (2) the shared fixture exact-count hunk. No behavioral interdependence between main's tool-result work and v6d. |
| Recommendation | **Keep and recut** on fresh branch `s2s-v6d-error-malformed-recut` (unique per `git ls-remote origin refs/heads/s2s-v6d-error-malformed-recut` → empty, 2026-08-25). |

The 2026-08-24 failure mode does not apply: no malformed-response outcome is on main.

## Changed-file list to carry over (from live head `c61abdd`, blob hashes in evidence §2)

- `go-agent-loop/pkg/probe/scenario_error_malformed_response.go` — carry verbatim
- `go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v6d-error-malformed-response-healthy-control.session.json` — carry verbatim
- `go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v6d-error-malformed-response-malformed.session.json` — carry verbatim
- `agent-cli/internal/cli/probe_test.go` additions (`TestProbeRunErrorMalformedResponseSuiteOfflineExitZero`, `TestProbeRunMalformedNegativeControlExitsNonZero`) — reapply onto current main's file
- `agent-cli/internal/cli/probe.go` v6d scenario registration lines — reapply onto current main's file
- `committed_fixtures_test.go` — do NOT copy; recompute exact count after adding 2 fixtures to a scanned root: expected `25` on today's main (`23 + 2`); run the test once and set the actual scanned number.

## Conflict notes

1. `agent-cli/internal/cli/probe.go`, one hunk: main added
   `deriveToolResultObservation(fixture string, observation *probe.ObservationSnapshot) error`
   scanning recorded tool-call lifecycle events; the PR adds its scenario registration in
   the same region. Textual union: keep main's function intact, add the v6d registration.
2. `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`, one hunk:
   constant `23` (main) vs `22` (#166 head). Resolution identical to the other recuts:
   recompute after adding this lane's two fixtures.

Driver for both: `0e8184d s2s-repair-v3b-bargein-pr164-conflict (#170)` plus `bb85450`
(fixture-count reconciliation) landing after the PR branched from `b08c6da`.

## Recut plan

1. `git fetch origin && git switch -c s2s-v6d-error-malformed-recut origin/main`
2. Carry the three new files verbatim from head `c61abdda5c8f02adfc74529ce20d286c90cb86b3`.
3. Reapply the two probe registration/test additions onto current main's versions.
4. Recompute the fixture count as above.
5. Lease: the six paths named here. No production session-loop code.

## Verification commands

```bash
# validator gate with recomputed count
cd go-llm-gateway && go test ./internal/sessionfixturevalidator/ -count=1 && cd ..

# v6d suite passes offline and exits zero; negative control still fails closed
go test ./agent-cli/internal/cli/ -run 'TestProbeRunErrorMalformedResponseSuiteOfflineExitZero|TestProbeRunMalformedNegativeControlExitsNonZero' -count=1 -v

# full probe package regression (scenario compiles into the measurable set)
go test ./go-agent-loop/pkg/probe/ -count=1
```

Note: the original lane PRD `tasks/todo/s2s-v6d-*.md` is absent from every ref in this
repository; commands reconstructed from the PR's own test symbols at head `c61abdd`.
