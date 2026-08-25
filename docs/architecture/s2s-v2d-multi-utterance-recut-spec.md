# Recut spec — s2s-v2d-audio-in-multi-utterance (#165)

Date: 2026-08-25. Evidence: `s2s-vertical-pr-disposition-triage-evidence.md` §1–§3,
table row in §9 of `s2s-program-status-2026-08-17.md`.

## Disposition

| axis | verdict |
|------|---------|
| Outcome already on main? | **No.** 5/6 changed files ABSENT_ON_MAIN; the 6th (`committed_fixtures_test.go`) is DIVERGENT only in the exact-count constant. Zero IDENTICAL blobs (evidence §2). |
| Conflict class | **Textual/mechanical.** One file, one hunk: fixture exact-count `23` (main) vs `22` (#165 head), driven by `bb85450` + `0e8184d` fixture reconciliation after the PR branched (evidence §3). No semantic overlap with the v2d feature. |
| Recommendation | **Keep and recut** on fresh branch `s2s-v2d-multi-utterance-recut` (unique per `git ls-remote origin refs/heads/s2s-v2d-multi-utterance-recut` → empty, 2026-08-25). |

The 2026-08-24 failure mode (recutting an outcome that already landed) does not apply:
no v2d multi-utterance artifact exists on main.

## Changed-file list to carry over (from live head `2090fbd`, blob hashes in evidence §2)

Carry all five new files verbatim:

- `agent-cli/test/integration/s2s_v2d_multi_utterance_test.go`
- `agent-cli/test/integration/testdata/s2s-v2d/s2s_v2d_multi_utterance.session.json`
- `agent-cli/test/integration/testdata/s2s-v2d/s2s_v2d_multi_utterance_merged.session.json`
- `agent-cli/test/integration/testdata/s2s-v2d/scenarios/s2s_v2d_multi_utterance.scenario.json`
- `agent-cli/test/integration/testdata/s2s-v2d/scenarios/s2s_v2d_multi_utterance_missegmented.scenario.json`

Do NOT copy the PR's `committed_fixtures_test.go` hunk. Main moved the constant to `23`;
after adding the two session fixtures + two scenario files under scanned root
`agent-cli/test/integration/testdata/**`, recompute and assert `27`
(`git show origin/main:go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`,
then run the test once to read the actual scanned number and set it exactly).

## Conflict notes

Single textual hunk in `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go`:

```
<<<<<<< origin/main
	if result.FilesScanned != 23 {
		t.Fatalf("... want exact count 23", ...)
=======
	if result.FilesScanned != 22 {
		t.Fatalf("... want exact count 22", ...)
>>>>>>> 2090fbd
```

Resolution: take main's structure, set the constant to the recomputed total (expected 27).
Nothing else conflicts; `git merge-tree` shows every other path auto-merges.

## Recut plan

1. `git fetch origin && git switch -c s2s-v2d-multi-utterance-recut origin/main`
2. Apply the five carried files from head `2090fbda32772c5fb918859a3fb29d90ecfa5eeb`
   (`git checkout 2090fbd -- <paths>` or cherry-pick `81711fc` dropping later count bumps).
3. Update the exact-count assertion as above.
4. Lease for the recut lane: the six paths in this spec plus the one-count assertion.
   Nothing outside; no production code.

## Verification commands

```bash
# unit-level validator gate (must pass with the recomputed count)
cd go-llm-gateway && go test ./internal/sessionfixturevalidator/ -count=1 && cd ..

# v2d behavior: one commit per utterance, missegmented negative control via real CLI
go test ./agent-cli/test/integration/ -run 'TestS2SV2DMultiUtteranceHappyPathOneCommitPerUtterance|TestS2SV2DMisSegmentedFixtureFailsViaCLI|TestS2SV2DSuiteSelectsEachFixtureByNameAndBothPassOrFailCorrectly' -count=1 -v
```

Note: the original lane PRD file `tasks/todo/s2s-v2d-*.md` is absent from every ref in
this repository (checked `git log --all --diff-filter=A -- 'tasks/todo/s2s-v*'`); these
commands are reconstructed from the PR's own test symbols at head `2090fbd`.
