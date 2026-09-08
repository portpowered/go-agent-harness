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
  `be9f11a5e6b9256df404fd236795098081237cbe`.
- The delivery tip may include this evidence-only checkpoint; the repair source
  and evidence below are pinned to this exact implementation checkpoint.
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
  source identity, repetition completeness, missing quiet metadata, expired
  quiet evidence, repeated package terminals, oversized durations/integers, and
  post-inventory source dirtiness. The regenerated `ctrl-be9f11a5/controls.json`
  reports 31 declared cases, 21 result groups, and zero Go/network/build
  invocations.
- The offline analyzer keeps assessment-specific package ranking and lane-wall
  evidence, while the canonical 60-second package-budget policy remains in
  `tools/timingate` and is not duplicated.
- Generated run group, repetition, and record names are bounded for Windows
  checkout portability. Superseded deep `controls-review-repair` artifacts
  were replaced by compact `ctrl` evidence; the longest new tracked relative
  path is 184 characters.
- `GOWORK=off go test . -count=1` in `tools/timingate`, Python AST parsing,
  `profile.py --help`, the 31-case control suite, and `git diff --check` pass.
- Fresh merged-main recheck from `be9f11a5` passed the same controls and focused
  checks at `2026-09-08T17:45:36Z`; the tracked controls report SHA-256 is
  `731bc9cbcfb39e1b82e7c2eec09dd8272658761e0f0c1f3487dde7dfd7f16053`.

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

The current canonical task rejection was for PR #403 head `87a92b3` and named
three C11 defects: legal repeated package start/terminal records were rejected;
`Elapsed=1000000000000` and oversized integers were not rejected with timingate
semantics; and run records copied the manifest source SHA without revalidating
current HEAD/dirty state. Commit `be9f11a5` repairs these causes, with each
boundary covered by the new public controls.

## Handoff

Push this same task, update PR #403 with the exact final head and evidence, and
return `ACCEPTED` to the script-owned CI gate without polling it. Any C11-owned
current-head rejection returns to this task; independent review, guarded merge,
and post-integration vertical validation remain external. All nine immutable
project gates remain open.
