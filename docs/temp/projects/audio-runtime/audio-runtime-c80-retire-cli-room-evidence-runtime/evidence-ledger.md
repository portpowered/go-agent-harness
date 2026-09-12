# C80 room evidence extraction

This is the admitted `audio-runtime/audio-runtime-v1` task
`audio-runtime-c80-retire-cli-room-evidence-runtime`. No second project or
acceptance waiver is used. Native Windows hardware/endpoints and physical or
acoustic proof are **OUT OF SCOPE** under the user amendment; Windows software
and hermetic replay remain required.

## identity and ancestry

- `prd.branchName`: `codex/audio-runtime-c80-retire-cli-room-evidence-runtime`;
  isolated worktree matches exactly.
- admission: `project-control.py verify-work --type task --name
  audio-runtime-c80-retire-cli-room-evidence-runtime` returned `admitted`.
- accepted behavior: `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`.
- startup integration: `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- planning origin/main: `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`.
- fetched review-time `origin/main`: `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`.
- baseline line census: 1,147 + 360 + 287 = 1,794 physical lines.
- no previous C80 review finding or evidence bundle was present; predecessor
  checkpoints and unrelated worktree changes were preserved.

## implementation

`go-agent-runtime/services/roomevidence` now exposes a host-neutral contract;
its private service owns output admission, bounded writers, WAV/PCM artifacts,
clock stamping, speech edges, mix finalization, latency, redaction, integrity,
status and first-error precedence. `wire` constructs independent service
instances without modifying the leased shared Wire registry. The CLI's three
owned production files are decision-free adapters/replay decode fixtures only.

The separate `external-consumer` module is tested with `GOWORK=off` and its Go
source imports only `roomevidence` and `roomevidence/wire`. It records two
participants, consumed sent/received audio, a tool diagnostic and an explicit
drop event, then checks finalized manifest/timeline/digest effects and typed
post-finalize rejection.

## focused evidence

- CLI room evidence focused suite: normal and race patterns passed; the
  recorder-produced room replay roundtrip passed 10 consecutive runs after
  timeline timestamp linearization; full affected agentruntime package and
  roundtrip regressions pass.
- Wire-backed public service suite: 6 tests across 3 packages passed;
  standalone consumer: 5 tests passed with `GOWORK=off`.
- measured retired CLI evidence files: 559 physical lines, leaving 1,235
  lines retired (limit 994; minimum retired 800).
- pinned vet, Staticcheck 2026.1, golangci-lint 2.9.0, coverage registration,
  and `make coverage-changed COVERAGE_BASE=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`
  pass. Repository architecture/Wire gates report only the unowned shared
  registry and downward baseline entries; those files remain unchanged pending
  exact lease release.
- the next required action after commit/push is script CI on this exact head;
  CI status is not claimed here and is not polled by the executor.
