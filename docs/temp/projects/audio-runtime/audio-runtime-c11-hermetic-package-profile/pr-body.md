# C11 hermetic package assessment

## Decision

`fresh_timing=BLOCKED`. No isolated or dedicated runner lease is available, so
no fresh Go inventory, warm build, or hermetic package timing was launched on
the shared factory host. `quiet-evidence-blocked.json` and the before/after
board, worker, process, and load snapshots are the immutable fallback. This is
not a performance PASS, waiver, or claim that the under-three-minute target is
met.

## Current candidate and admission

- Project/work: admitted `audio-runtime` / `audio-runtime-c11-hermetic-package-profile`.
- Session/server: `~default` / `http://127.0.0.1:7439`.
- Branch and `prd.json.branchName`: `codex/audio-runtime-c11-hermetic-package-profile`.
- Current delivery implementation checkpoint:
  `59f370b6239744a2bf6d7234097a10cbd9f364fe`.
- The implementation repair and exact-head controls are committed at
  `59f370b6239744a2bf6d7234097a10cbd9f364fe`; the evidence checkpoint adds
  only owned evidence/provenance updates.
- Integrated `origin/main`: `668f2d8816beaa078d058b3f0bcc59600b71a023`.
- Startup integration and baseline ancestors remain
  `8bdafc7f947a3a2c9856220abdc539437035bd21` and
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.

## Repairs and focused evidence

- Offline analysis now enforces the same quiet-evidence contract as heavy
  commands: current `captured_at_utc`/`valid_until_utc`, runner metadata,
  before/after `active_work`/process/load observations, captured summary
  provenance, manifest-root containment, command timing metadata, and
  inventory-consistent no-test markers before accepting fresh timing.
  Captured head, repository, and dirty-path provenance is cross-checked against
  the manifest; malformed records fail closed and atomically replace stale
  analysis output with an `INVALID` artifact.
- The public controls cover cross-package concurrency, fail-closed heavy
  admission, stdout/stderr/quiet-artifact containment, raw-artifact truth,
  source identity including forged captured validation, repetition completeness,
  missing quiet metadata, expired quiet evidence, repeated package terminals,
  oversized durations/integers, post-inventory source dirtiness, missing timing,
  weak quiet observations, out-of-root caches, no-test conflicts, malformed
  records, and stale-analysis replacement. The regenerated
  `ctrl-59f370b/controls.json` reports 47 declared cases, 23 result groups, and
  zero Go/network/build invocations; its SHA-256 is
  `af631fa173222da2ae78ec67047ee9c1fa06a95e73da28260211e3283912853c`.
  The new regressions reject unreferenced invalid/zero-request groups,
  incomplete command-record schemas and artifacts, forged retained
  source-validation output/identity, and aggregate duration overflow.
- The offline analyzer keeps assessment-specific package ranking and lane-wall
  evidence, while the canonical 60-second package-budget policy remains in
  `tools/timingate` and is not duplicated.
- Generated run group, repetition, and record names are bounded for Windows
  checkout portability. Superseded deep `controls-review-repair` artifacts
  were replaced by compact `ctrl` evidence; the longest new tracked relative
  path in this report is 209 characters.
- `GOWORK=off go test . -count=1` in `tools/timingate`, Python AST parsing,
  `profile.py --help`, the 47-case control suite, and `git diff --check` pass.
- Fresh exact-head recheck from `59f370b6` passed the same controls and focused
  checks at `2026-09-08T20:48:35.484Z`; the tracked controls report SHA-256 is
  `af631fa173222da2ae78ec67047ee9c1fa06a95e73da28260211e3283912853c`.

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

The latest current-head rejection is run `34270089010`, job `102209193353`,
at submitted head `bc7609b5`. Its full log and job metadata are retained in
`ci-rejection-current.log` and `ci-rejection-current.json`. The hermetic job
failed the existing `TestSessionCLI_DuplexPCMMultiTurnSchedule` positive
harness and the
`TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls/missing_commit`
positive baseline before its negative mutation. Both paths are outside C11's
owned directories. Bounded local rechecks of the tool-barge oracle and both
multi-turn tests passed; this is not a claim that hosted CI is green or that
C11 owns a runtime repair.

The canonical review inbox through review attempt 27 named unreferenced
invalid groups, forged retained Git metadata, aggregate duration overflow,
incomplete command records, stale head/evidence references, and the preceding
timing/quiet/cache/no-test/repeated-terminal/artifact controls. Commit
`59f370b6` repairs the remaining code causes, and the current public controls
cover every finding.

## Handoff

Commit/push this same task, update PR #403 with the exact final head and
evidence, and return `ACCEPTED` to the script-owned CI gate without polling it.
Any C11-owned
current-head rejection returns to this task; independent review, guarded merge,
and post-integration vertical validation remain external. All nine immutable
project gates remain open.
