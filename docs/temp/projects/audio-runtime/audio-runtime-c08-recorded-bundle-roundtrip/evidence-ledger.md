# audio-runtime-c08-recorded-bundle-roundtrip evidence ledger

Task: `audio-runtime-c08-recorded-bundle-roundtrip`
Project: admitted `audio-runtime`; factory session `~default`; branch `codex/audio-runtime-c08-recorded-bundle-roundtrip`.

## Admission and ancestry

- `cwd`: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness`
- command: `rtk proxy python3 factory/scripts/project-control.py verify-work --type task --name audio-runtime-c08-recorded-bundle-roundtrip`
- result: `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c08-recorded-bundle-roundtrip"}`
- `prd.json.branchName`: `codex/audio-runtime-c08-recorded-bundle-roundtrip`; exact worktree branch matches.
- current fetched `origin/main` and pre-change `HEAD`: `00c147585f31808c7bdbbf6051cc9df423dbdf37`.
- required startup pin `8bdafc7f947a3a2c9856220abdc539437035bd21`, baseline pin `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and `origin/main` are ancestors of the isolated worktree. `git fetch origin main` completed before implementation.
- implementation checkpoint: `71986c13db2dedfdc7b794111b751cb5bf354554` (`fix: restore recorded audio bundle replay`). Rollback checkpoint remains `00c147585f31808c7bdbbf6051cc9df423dbdf37`; no merge/reset/host-checkout operation was used.

## Baseline reproduction

`cwd`: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c08-recorded-bundle-roundtrip/agent-cli`

Command, bounded by 20 seconds:

```text
rtk go run ./cmd/testtimeout --timeout 20s --dir /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/docs/temp/probes/audio-runtime-c07-stabilized-runtime-probe-fixture-corrected -- /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/docs/temp/probes/audio-runtime-c07-stabilized-runtime-probe-fixture-corrected/artifact-0 session replay /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/docs/temp/probes/audio-runtime-c07-stabilized-runtime-probe-fixture-corrected/evidence/runs/c07-recorded-replay-v4
```

Expected and actual: exit `1`; stdout empty; stderr contains exactly:

```text
Error: replay bundle is incomplete: missing timeline.jsonl in .../evidence/runs/c07-recorded-replay-v4 or .../evidence/runs/c07-recorded-replay-v4/audio-trace
```

The preserved baseline executable SHA256 is `c1a03f25b11a941c4df0a076bddc7f5cabc73e192481095a19b344d60be1a7fe`. The preserved provider capture and missing-timeline bundle were not modified.

## Candidate public round trip

Final candidate source was built from this worktree at implementation checkpoint `71986c13db2dedfdc7b794111b751cb5bf354554` with:

```text
cwd: /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c08-recorded-bundle-roundtrip/agent-cli
rtk go build -o /var/folders/p0/39h9prbs7pn446h35_zhpr300000gn/T/tmp.70REIdzn7u/yui-candidate ./cmd/yui
```

Build exit `0`; final candidate binary SHA256 is `40a02ec2e8c72211532c09e55af601665f469b61a9f832ba39ddeb4231e24f26`.

Fixture command, `cwd` `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/docs/temp/probes/audio-runtime-c07-stabilized-runtime-probe-fixture-corrected`, bounded by the fixture's candidate run:

```text
rtk /var/folders/p0/39h9prbs7pn446h35_zhpr300000gn/T/tmp.70REIdzn7u/yui-candidate session --replay evidence/captures/c07-audio-tool-traced.session.json --audio-out /var/folders/p0/39h9prbs7pn446h35_zhpr300000gn/T/tmp.kAIPXPRed4/continuation.pcm --record-dir /var/folders/p0/39h9prbs7pn446h35_zhpr300000gn/T/tmp.kAIPXPRed4/bundle --trace-audio
```

Expected: credential-free public replay capture exits `0`, writes a complete directory with `audio-trace/timeline.jsonl`, preserves provider/tool ordering and output PCM. Actual exit `0`; raw operator output:

```text
Tool result: PROBE_TOOL_MARKER_9182

Assistant: strict replay continuation

[session closed: fixture_complete]
[session terminal: classification=provider_close terminal_reason=provider_close terminal_provenance=provider output_state=not_applicable]
```

The candidate bundle contains `provider.json`, `session-log.jsonl`, `audio/out-000.pcm`, `audio-trace/timeline.jsonl`, and `audio-trace/speaker-enqueued.wav`. The candidate trace has `18` provider wire events, one `tool_call`, one `tool_result`, five 480-sample 24 kHz provider-rate speaker frames at start samples `0,480,960,1440,1920`, and a clean `recording_closed` event. The rendered output remains 16 kHz.

Candidate input fixture SHA256: `38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169`.
Candidate output `audio/out-000.pcm` SHA256: `0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`.
Candidate trace `speaker-enqueued.wav` SHA256: `305d40c0fa1b687133be6a7841654dcbe89310b7b7f9800cb624c25bcffd880c`.
Candidate continuation output SHA256: `7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`.

Replay command against the candidate bundle is bounded by 20 seconds:

```text
cwd: /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/docs/temp/probes/audio-runtime-c07-stabilized-runtime-probe-fixture-corrected
rtk /var/folders/p0/39h9prbs7pn446h35_zhpr300000gn/T/tmp.70REIdzn7u/yui-candidate session replay /var/folders/p0/39h9prbs7pn446h35_zhpr300000gn/T/tmp.kAIPXPRed4/bundle
```

Actual exit `0`; raw output: `strict replay continuationReplay verified: 18 wire events, 1 tool calls. Recorded render audio: false; render tap unavailable: false.`

## Focused and accumulated gates

- `rtk go test ./agent-cli/internal/transport/cli/internal/livehost ./agent-cli/internal/services/internal/replay`: `Go test: 37 passed in 2 packages`; the exact `-tags=nomicrophone` C08 package command also passed `37` tests in `3` packages.
- `rtk go test ./agent-cli/internal/transport/cli/... ./agent-cli/internal/services/internal/... ./go-agent-runtime/services/recording/... ./go-agent-runtime/services/replay/...`: `Go test: 1899 passed in 15 packages` on the final tree.
- Final C08 normal/race package checks passed `93`/`93` tests in `7` packages; the final focused cross-module race passed `130` tests in `9` packages; embedding race passed; and the embedding dependency inventory returned only `example.com/agent-runtime-consumer`.
- `rtk bash scripts/test-session-ci-regressions.sh normal`, `race`, and `coverage` all passed with the existing `count=3` and preserved 20-trial high-rate subtests; expected replay/audio negative-control diagnostics remained asserted.
- Pinned `make lint` and `make staticcheck` passed (`golangci-lint 2.9.0`, `staticcheck 2026.1`); `make fmt`, `make build`, `make wire-check`, and `make vet` passed.
- `rtk git diff --check`: clean.
- Controls: missing `timeline.jsonl` remains rejected with `replay bundle is incomplete`; live trace unit controls retain staged evidence and do not attach a trace when provider capture is missing or corrupt.
- Candidate-only `rtk make architecture-size-check ARCHITECTURE_BASE=` passed: `181 package(s), 1853 file(s), 26924 function(s) checked`. The configured default comparison reports only the inherited mainline `baseline-history-source` mismatch (`ddebb8f...` versus merge base `00c147...`); no new candidate architecture/size issue remains and the out-of-lease baseline file was not changed.
- Final candidate `go build` and public replay round trip were rerun after the architecture-compliance and lint repairs; no CI was polled or claimed green.

No CI was polled or claimed green. The next checkpoint is push `71986c13db2dedfdc7b794111b751cb5bf354554`, open/update the same task PR against `main`, verify submission to script CI without polling terminal CI, and retain the task for any exact CI rejection.
