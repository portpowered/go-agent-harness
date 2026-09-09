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
  `aeef9126ae54ea925dabb260818082db8675b324`.
- The implementation and exact-head controls are committed against that
  candidate; the evidence report is generated from that exact implementation
  SHA and packaged separately. This checkpoint consolidates all phase command records into one
  `manifest.commands` array, validates the whole input before `--group`
  display filtering, binds Git/test command identity, enforces warm/full/cohort
  scheduling, and records the old-to-new mapping in `schema-control-mapping.md`.
  It also rejects contradictory timeout/status/exit/signal/spawn declarations
  before analysis. The current repair additionally rejects omitted packages from
  a hermetic full trial, missing or mismatched successful warm binaries, and
  wall time forged independently of the monotonic interval. Streaming capture
  closes the OpenAI response body before the terminal event and the CLI stream
  before Save/Flush.
- Integrated `origin/main`: `c3bb663e118de9e73ea3eb211b381e8f86c4f480`.
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
  records, and stale-analysis replacement. The preserved predecessor
  `ctrl-c11-repair/controls.json` reports 63 cases;
  the current `ctrl-aeef9126/controls.json` reports 67 declared cases, 27 result
  groups, and zero Go/network/build invocations; its SHA-256 is
  `52226b0accd97a785c93589ea6ec13ec0fd82ea7ff787ce15c98daf082cddef3`.
  The new regressions reject unreferenced invalid/zero-request groups,
  incomplete command-record schemas and artifacts, forged retained
  source-validation output/identity, aggregate duration overflow, malformed
  canonical phase records and warm summaries, and hermetic captures that skip
  warm-up. The new timed-out/PASS mutation is rejected before aggregation.
  Lifecycle regressions reject cohort repeat 3, failed full trials,
  partial full-trial module coverage, and non-PASS inventory/warm commands
  before any Go command can start. The valid hermetic baseline controls also
  reject omitted full-inventory packages, deleted warm binaries, and forged
  wall/monotonic timing.
- The offline analyzer keeps assessment-specific package ranking and lane-wall
  evidence, while the canonical 60-second package-budget policy remains in
  `tools/timingate` and is not duplicated.
- Generated run group, repetition, and record names are bounded for Windows
  checkout portability. Superseded deep `controls-review-repair` artifacts
  were replaced by compact `ctrl` evidence; the longest new tracked relative
  path in this report is 209 characters.
- `GOWORK=off go test . -count=1` in `tools/timingate`, Python AST parsing,
  `profile.py --help`, the 67-case control suite, focused capture/provider/
  recorder/session/agent-loop Go tests, and `git diff --check` pass. The
  barrier capture/replay pair also passed with `-count=10`.
- Fresh exact-head recheck from `aeef9126` passed the same controls and focused
  checks; the tracked controls report SHA-256 is
  `52226b0accd97a785c93589ea6ec13ec0fd82ea7ff787ce15c98daf082cddef3`.

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

The latest inspected current-head rejection is run `34277521278`, job
`102234088178` (`CI (coverage)`) at submitted head
`43519903572c4047bbb0068d9ed73e7027a257d7`. Eight required jobs passed; the
coverage job failed the existing
`TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnAudioControl` because its
positive harness-A baseline exceeded the two-second response deadline with
`context deadline exceeded`. The exact metadata and 367-line log are retained
in `ci-rejection-34277521278.json` and `ci-rejection-34277521278.log`. This
path is outside C11's owned directories; this is not a claim that hosted CI is
green or that C11 owns a runtime repair.

The latest inspected predecessor rejection is run `34294562855`, job
`102288251336` (`CI (coverage)`) at head
`6079ce29b98e553514c2aeb82e78a762f0131faa`. Eight required jobs passed; the
coverage job failed
`TestAskRecordsAndReplaysThroughProviderService/stream=true` at
`agent-cli/test/integration/ask_capture_test.go:58` because capture flush found
an active HTTP response body. Exact metadata and the 82-line log are retained
in `ci-rejection-34294562855.json` and `ci-rejection-34294562855.log`. That
rejection supplied the current C11 repair target; the deterministic barrier
regression now covers the causal ordering. The new candidate is still not a
green-CI claim.

The canonical review inbox through review attempt 37 named the timed-out/PASS
process-status mismatch in addition to stale exact-head evidence, cohort repeat
overrun, failed/partial full-trial admission, non-PASS
inventory/warm phase commands, unreferenced invalid groups, forged retained Git
metadata, aggregate duration overflow, incomplete command records, and the
preceding timing/quiet/cache/no-test/repeated-terminal/artifact controls. Commit
`aeef9126` contains the remaining code repairs, and the current public controls
cover every finding.

## Handoff

Commit/push this same task, update PR #403 with the exact final head and
evidence, and return `ACCEPTED` to the script-owned CI gate without polling it.
Any C11-owned
current-head rejection returns to this task; independent review, guarded merge,
and post-integration vertical validation remain external. All nine immutable
project gates remain open.
