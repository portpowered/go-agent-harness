# C166 session-turns recovery handoff

Project: `audio-runtime` (`audio-runtime-v1`)

Task: `audio-runtime-c166-recover-c150-session-turns-handoff` (`work-task-90`)

This is an executor handoff record. It does not claim script CI green, independent
review, guarded merge, project acceptance, or the required post-merge vertical.

## Admission and preserved ancestry

Admission was verified from `FACTORY_ROOT` with:

```text
project-control.py verify-work --type task --name audio-runtime-c166-recover-c150-session-turns-handoff
{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c166-recover-c150-session-turns-handoff"}
```

The isolated worktree branch is
`codex/audio-runtime-c166-recover-c150-session-turns-handoff`, matching
`prd.json.branchName` exactly. The worktree was clean at admission on planning
main `2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`; `git fetch origin main` was
performed before integration and did not touch the running host checkout.

The authoritative C150 checkpoint was preserved in its separate worktree and
was clean at `0eb675da0eefcc09cac58104b7f416ed6abe61c5`. The recovered candidate
retains the C87 history (historical PR 476 head
`28b5a9b18f67a4343ef9e12141ad5e5fc84ef18f`), the C136 checkpoint
`7268f46a8c23cea480aecba621d49c01e4a30aac`, and the C150 checkpoint. The
current candidate integrates C150 with merge commit
`eb9a4691c2c35c0508ce2135726e9685b91b6501`; no reset or history rewrite was
used. Ancestry checks passed for startup
`8bdafc7f947a3a2c9856220abdc539437035bd21`, C136, C150, accepted main
`97d3dcfb1e97a2611aa26b203a7f893442db4768`, planning main, and the fetched
`origin/main`.

The existing PR is #513. C110/C111/C119 were terminal delivery attempts with
open historical PRs and no active writer for these session-turns paths; active
C164 retains the exact captureclaim-only lease. No second project, task, or PR
was created.

## Exact bounded repair

The merge checkpoint reproduced exactly thirteen known shared findings: twelve
stale downward session-turns baseline records and the unregistered generated
Wire file. Commit `10d6e56e1892b4b9fc32e402c62315880e80f2eb` applies only those
demonstrated repairs:

- one `go-agent-runtime/services/sessionturns/wire` line in
  `scripts/wire-packages.txt`;
- one exact `services/sessionturns/wire/wire_gen.go` generated-file object in
  `docs/architecture/architecture-policy.json`;
- exactly the nine named mutable-global records and one named
  `readTurnResponse` record in `session_turns.go.json`; and
- exactly the named lifecycle-test complexity record and `noTurnSetup` record
  in `session_turns_test.go.json`.

The C136 verifier was extended in `ccd973258` with an exact C166 evidence-path
allowlist and content checks for those four shared files. It rejects unrelated
shared edits, duplicate registry/policy entries, metadata changes, and any
other baseline deletions. The verifier's existing mutation, retirement, public
contract, and consumer checks remain enabled.

## Focused and accumulated gates

All commands below were run against the repaired candidate before handoff:

- `go test ./go-agent-runtime/services/sessionturns/... -count=3 -timeout=240s` — 48 passed in 3 packages.
- The same session-turns packages with `-race -count=3` — 48 passed in 3 packages.
- CLI session-turns normal `-run 'SessionTurns' -count=3` — 6 passed; race `-count=1` — 2 passed.
- GOWORK-off external public consumer — passed.
- The bounded public runner with `credential-free-audio-tool` and `existing-interruption-tool-regression` — 3/3 per case, no survivors, output bounded, credentials scrubbed.
- `COUNT=1 scripts/test-session-ci-regressions.sh all` — normal, coverage, race, transport, integration, device-gateway, and composed LLM regression lanes passed, including the negative controls and 20/20 high-rate trials in each mode.
- `make fmt`, focused `go vet`, `make coverage-registration`, `make wire-check`, `make architecture-size-check`, `git diff --check`, and pinned `make staticcheck` — passed; architecture reports 205 packages, 1949 files, and 28826 functions.
- Pinned `make lint` — 0 issues in all 15 configured modules.
- Coverage produced all six module profiles plus the embedding profile. The final coverage gate using those exact profiles passed: `195 registered packages checked across 7 profiles`.

The three C136 verifier modes (`mutations`, `retirement-and-adapter`, and
`owned-and-excluded-paths`) passed with shared-repair, retirement, contract,
consumer, and mutation checks enabled. The final tree is clean.

## Handoff

The next action is to push this candidate to the existing PR #513 remote head
without force or a new PR, update its description with this evidence path, and
return `ACCEPTED` once the exact pushed revision is verified. Script CI then owns
the changed-head gate; any rejection must return to this same task for exact
diagnosis and repair. Independent review, guarded merge, and the fresh immutable
vertical probe remain downstream.
