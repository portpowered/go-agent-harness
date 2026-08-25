# Recut spec — s2s-v2e-audio-in-truncated (#167)

Date: 2026-08-25. Evidence: `s2s-vertical-pr-disposition-triage-evidence.md` §1–§5,
table row in §9 of `s2s-program-status-2026-08-17.md`.

## Disposition

| axis | verdict |
|------|---------|
| Outcome already on main? | **No.** All 8 new artifacts (3 probe fixtures, 3 probe scenarios, the proof doc, and its status-doc section) are ABSENT_ON_MAIN; the 6 framework files are DIVERGENT (PR adds a second expectation family; main added a different one) (evidence §2). |
| Conflict class | **Textual hunks at shared extension points, resolution is a semantic union.** 7 content hunks across `probe.go` (3), `deadguard.go` (1), `expect.go` (3): main's tool-result expectation family (`ExpectToolResultDelivered`, `ExpectToolResultDiscarded`, `ExpectNoOrphanedToolResult`, from #170/#164) collides with the PR's buffer-disposition family (`ExpectBufferDisposition`) in the same alias map, evaluation switch, and deadguard allowlist. The textual union is mechanical, but the recut must keep BOTH families alive — dropping either side's cases silently weakens an existing or the new invariant. |
| Recommendation | **Keep and recut** on fresh branch `s2s-v2e-audio-in-truncated-recut` (unique per `git ls-remote origin refs/heads/s2s-v2e-audio-in-truncated-recut` → empty, 2026-08-25). |

The 2026-08-24 failure mode does not apply: no truncated-audio outcome is on main.

## Red checks — inherited, not owned

#167's failing `ci` / `CI (hermetic)` runs fail in
`TestAllCommittedSessionFixturesPassWithExactCount` with "scanned **20**, want exact
count **18**" — byte-identical to the failure at its merge base `81267d6e` (evidence §4).
The PR adds no file inside any scanned root (its fixtures live under
`agent-cli/internal/cli/testdata/**`, outside `allCommittedFixtureRoots()`); its single
commit did not cause the failure. Rebasing onto current main (green at count 23)
resolves it with zero code change.

## Changed-file list to carry over (from live head `79a54e5`, blob hashes in evidence §2)

Carry verbatim (all ABSENT_ON_MAIN):

- `agent-cli/internal/cli/testdata/probe-fixtures/s2s_v2e_audio_in_truncated_16k.session.json`
- `agent-cli/internal/cli/testdata/probe-fixtures/s2s_v2e_audio_in_truncated_24k.session.json`
- `agent-cli/internal/cli/testdata/probe-fixtures/s2s_v2e_audio_in_truncated_uncommitted.session.json`
- `agent-cli/internal/cli/testdata/probe-scenarios/s2s-v2e-audio-in-truncated-16k.scenario.json`
- `agent-cli/internal/cli/testdata/probe-scenarios/s2s-v2e-audio-in-truncated-24k.scenario.json`
- `agent-cli/internal/cli/testdata/probe-scenarios/s2s-v2e-audio-in-truncated-uncommitted-negative.scenario.json`
- `docs/architecture/s2s-v2e-audio-in-truncated-proof.md`

Reapply onto current main's versions (DIVERGENT):

- `go-agent-loop/pkg/probe/expect.go` — add the `BufferDisposition` kind constants,
  docs, and evaluation switch cases alongside (not instead of) the tool-result cases.
- `go-agent-loop/pkg/probe/deadguard.go` — allowlist must end up containing BOTH
  `ExpectToolResultDelivered, ExpectToolResultDiscarded, ExpectNoOrphanedToolResult`
  AND `ExpectBufferDisposition`.
- `go-agent-loop/pkg/probe/expect_test.go` — add `TestEvaluateBufferDisposition` and
  table entries; keep main's existing tests untouched.
- `go-agent-loop/pkg/probe/scenario_measurable.go` — one-line scenario registration.
- `agent-cli/internal/cli/probe.go` — expectation alias map keys
  (`buffer_disposition`/`buffer-disposition`) plus the observation-derivation call site;
  keep main's `deriveToolResultObservation` call intact alongside
  `replayBufferDisposition`.
- `agent-cli/internal/cli/probe_test.go` — PR-side additions only.
- `docs/architecture/s2s-program-status-2026-08-17.md` — re-add the PR's dated section
  as a fresh append after whatever sections main has by then (append-only discipline;
  never edit pre-existing lines).

No scanned-root fixture is added by this lane: the validator exact-count assertion stays
at whatever main asserts on the rebase day (23 today) — verify by running it, adjust only
if the scanned number differs from the asserted number.

## Conflict notes

All seven hunks come from one driver pair landing after this PR branched:
`0e8184d s2s-repair-v3b-bargein-pr164-conflict (#170)` (+ fixture reconciliation
`bb85450`). Representative hunks:

```
# deadguard.go — main vs PR allowlist arms
<<<<<<< origin/main
	ExpectToolResultDelivered, ExpectToolResultDiscarded, ExpectNoOrphanedToolResult:
=======
	ExpectBufferDisposition:
>>>>>>> 79a54e5
```

```
# probe.go — derivation call site
<<<<<<< origin/main
		if deriveErr := deriveToolResultObservation(fixture, &observation); deriveErr != nil {
			return probe.ObservationSnapshot{}, deriveErr
		}
=======
		observation.BufferDisposition = replayBufferDisposition(fixture)
>>>>>>> 79a54e5
```

Resolution rule everywhere: **union**. Main's arms stay; append the PR's arms. The
evaluation switch gains both case families; the alias map gains both key sets.

## Recut plan

1. `git fetch origin && git switch -c s2s-v2e-audio-in-truncated-recut origin/main`
2. Carry the seven verbatim files from head `79a54e5790a417bfaaf0807a05f668426b94c9ad`.
3. Reapply the framework additions per the union rule above.
4. Re-append the proof/status-doc section fresh.
5. Lease: the fourteen paths named here plus the status-doc append. No production code.

## Verification commands

```bash
# expectation framework: both families green together (the semantic-union check)
go test ./go-agent-loop/pkg/probe/ -run 'TestEachMeasurableExpectationPassesAndFails|TestEvaluateBufferDisposition|TestMalformedExpectationsHaveTypedValidationIdentity' -count=1 -v

# probe CLI surface still compiles and passes with both derivations wired
go test ./agent-cli/internal/cli/ -count=1

# validator gate unchanged (no scanned-root additions expected)
cd go-llm-gateway && go test ./internal/sessionfixturevalidator/ -count=1 && cd ..

# offline scenario run through the real probe CLI (shape from the proof doc)
go run ./agent-cli/cmd/agent probe --scenario s2s-v2e-audio-in-truncated-16k --offline
```

Note: the original lane PRD `tasks/todo/s2s-v2e-*.md` is absent from every ref in this
repository; commands reconstructed from the PR's own test symbols at head `79a54e5` and
the committed proof document carried in step 2. Confirm the exact `probe` subcommand
invocation against `agent-cli/internal/cli/probe.go` on the recut branch before relying
on the last line.
