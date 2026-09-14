# C173 session-turns CI handoff

Project: `audio-runtime` (`audio-runtime-v1`)

Task: `audio-runtime-c173-recover-c166-session-turns-ci-handoff` (`work-task-110`)

This is executor handoff evidence. It does not claim script-CI success,
independent review, guarded merge, vertical acceptance, or project acceptance.

## Admission and ancestry

The required Factory policy, implementation handoff, admitted manifest,
`prd.json`, `progress.txt`, preserved C166 evidence, and canonical `~default`
board were read before action. Exact admission returned:

```text
{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c173-recover-c166-session-turns-ci-handoff"}
```

The C173 worktree is clean on
`codex/audio-runtime-c173-recover-c166-session-turns-ci-handoff`, matching
`prd.json.branchName`. The tested source candidate was
`2e6af0f09ce398d3ad99aed92b5e0d3d3b222932`; the pushed descendants
`8b0f599134a19db4db396c728882bf3d961c730e` and
`95d09711245ab1323c60998302209adf93445abb` are evidence-only, with no
executable input changed between those revisions. The final handoff verifies
the exact current `HEAD` and remote PR SHA separately.
It adopted C166 with a fast-forward to the preserved PR 513 head
`034388127ab34f1ffc9e31000a14235eb4255741`, then merged the freshly fetched
current `origin/main` `8490f8dcad63adde99036016e1e7ffd9ecf61e34` and its subsequent
current-main commits through `1b1c0296b9471b930c2b290f3bf6fa10559957de` using
non-rewriting merge commits `5d11fd75` and `2e6af0f`. Startup
`8bdafc7f947a3a2c9856220abdc539437035bd21`, planning main
`2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`, C166 head, and current main are
ancestors. The preserved C166 worktree remains clean at `034388127`.

The old exact-head run `34790849392` is recorded as nine-for-nine green on
`034388127`; it is superseded for handoff by the current-main merge, so no
green CI result is claimed for `2e6af0f` or its evidence-only descendant.
PR 513 remains the existing PR; no
replacement PR or workflow was created.

## Focused and accumulated evidence

Against executable source `2e6af0f` (unchanged by the evidence-only commit):

- `go test ./go-agent-runtime/services/sessionturns/... -count=3 -timeout=240s`:
  48 passed in 3 packages.
- The same sessionturns packages under `-race -count=3`:
  48 passed in 3 packages.
- CLI `SessionTurns` compatibility: 6 normal and 2 race tests passed.
- Preserved C136 verifier modes `owned-and-excluded-paths`,
  `retirement-and-adapter`, and `mutations`: passed. The verifier lives under
  the C136 evidence namespace; the copied C150 path named in the PRD does not
  exist and was not created or substituted.
- `GOWORK=off` external consumer: passed.
- Credential-free audio/tool and existing interruption/tool public regressions:
  both passed 3/3 with bounded output, reaped children, and no survivors.
- `COUNT=1 bash scripts/test-session-ci-regressions.sh all`: normal, coverage,
  and race modes passed, including the 20 high-rate trials and retained
  negative controls.
- `make build`, `make fmt`, `make vet`, pinned `make lint` (0 issues in all 15
  modules), pinned `make staticcheck`, `make wire-check`,
  `make architecture-size-check`, `make coverage-registration`, and
  `git diff --check`: passed. Architecture reports 205 packages, 1949 files,
  and 28836 functions; coverage registration reports 195 workspace packages
  across 6 modules; Wire regeneration left the tree unchanged.

The candidate changes only the preserved C166 session-turns delivery plus the
required current-main ancestry. No session-turns causal defect or prior review
finding was demonstrated; C166/C150 records report no prior PR review, and no
new review row exists yet. Native Windows hardware/endpoints and physical
acoustics remain out of scope, never PASS. The next state transition is the
canonical SCRIPT CI gate on the genuinely changed pushed head recorded by the
final handoff verification; CI owns polling and any exact rejection returns to
this same task.
