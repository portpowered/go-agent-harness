# C11 hermetic package assessment

## Decision

`fresh_timing=BLOCKED`. C08 owns the shared factory host and no isolated or
dedicated runner lease is available, so no fresh Go inventory, warm build, or
hermetic package timing was launched. `quiet-evidence-blocked.json` and the
before/after board, worker, process, and load snapshots are the immutable
fallback. This is not a performance PASS, waiver, or claim that the
under-three-minute target is met.

## Current candidate and admission

- Project/work: admitted `audio-runtime` / `audio-runtime-c11-hermetic-package-profile`.
- Session/server: `~default` / `http://127.0.0.1:7439`.
- Branch and `prd.json.branchName`: `codex/audio-runtime-c11-hermetic-package-profile`.
- Current implementation/control repair checkpoint:
  `39d177009bfca1ada9b01d5a64a071fce9d69f44`.
- The final submitted PR head is verified immediately before script-CI handoff;
  the repair source and evidence below are pinned to the merged checkpoint above.
- Integrated `origin/main`: `668f2d8816beaa078d058b3f0bcc59600b71a023`.
- Startup integration and baseline ancestors remain
  `8bdafc7f947a3a2c9856220abdc539437035bd21` and
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.

## Repairs and focused evidence

- Offline analysis now enforces the same quiet-evidence contract as heavy
  commands: current `captured_at_utc`/`valid_until_utc`, runner metadata,
  before/after load/lease observations, captured summary provenance, and
  manifest-root containment before reading.
- The public controls cover cross-package concurrency, fail-closed heavy
  admission, stdout/stderr/quiet-artifact containment, raw-artifact truth,
  source identity, repetition completeness, missing quiet metadata, and expired
  quiet evidence. The regenerated `ctrl-current/controls.json` reports 27 declared scenarios, 17 result
  groups, and zero Go/network/build invocations.
- The offline analyzer keeps assessment-specific package ranking and lane-wall
  evidence, while the canonical 60-second package-budget policy remains in
  `tools/timingate` and is not duplicated.
- Generated run group, repetition, and record names are bounded for Windows
  checkout portability. Superseded deep `controls-review-repair` artifacts
  were replaced by compact `ctrl` evidence; the longest new tracked relative
  path is 184 characters.
- `GOWORK=off go test . -count=1` in `tools/timingate`, Python AST parsing,
  `profile.py --help`, the 27-control suite, and `git diff --check` pass.
- Fresh merged-main recheck from `39d17700` passed the same controls and focused
  checks at `2026-09-08T17:03:47Z`; the tracked controls report SHA-256 is
  `0c0f17a3850cb95605df4f358881301bedd99afe645988d54224a966dbc900e3`.

## CI rejection disposition

The latest prior pre-C12 current-head rejection is run `34231535549`, job `102078631796`
(`CI (hermetic)`) at submitted head `0f8529a3`. Eight required jobs passed, but the
existing integration suite failed
`TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`
because `session_tool_audio_remote_e2e_test.go:182` did not observe the final
PCM marker before the scenario deadline. The exact metadata and failure lines
are in `ci-rejection-hermetic.json`. This path is outside the C11 lease and is
owned by the C12 runtime task, which is now merged at `668f2d88`; this C11
repair does not claim that historical result as current-head CI.
The new C11 head is ready for script-owned CI; no result for that new head is
claimed here. The earlier Windows checkout rejection and its portable-path
repair remain in `ci-rejection-windows.json`.

## Handoff

Push this same task, update PR #403 with the exact final head and evidence, and
return `ACCEPTED` to the script-owned CI gate without polling it. Any C11-owned
current-head rejection returns to this task; independent review, guarded merge,
and post-integration vertical validation remain external. All nine immutable
project gates remain open.
