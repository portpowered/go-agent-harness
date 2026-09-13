# C156 agentsession coverage repair evidence

Task: `audio-runtime-c156-repair-agentsession-coverage-regression`

This evidence is bounded to the admitted `audio-runtime` project and the two
owned paths. The Go mutation is `agent-cli/internal/services/agentsession/coverage_c156_test.go`.
No production file, C145 path, manifest, threshold, exclusion, or retired CLI
policy was changed.

## Admission and ancestry

- `project-control.py verify-work --type task --name audio-runtime-c156-repair-agentsession-coverage-regression --root "$FACTORY_ROOT"`: exit 0, `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c156-repair-agentsession-coverage-regression"}`.
- Factory session: `~default`; startup `integrationRevision`: `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- Planning `origin/main`: `4a1c399ccbb3d780be95eb04316e84b8f11a6646`; review-time fetched current `origin/main`: `97d3dcfb1e97a2611aa26b203a7f893442db4768`.
- Baseline ancestor: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.
- C145 comparison head: `d014a3586368c37e20618481ee162e1b83db113f`.
- Source plan SHA-256: `f715163fb20f46a18837d4a4d19ff6d880aaadf8dbf40acfff88a0a6c5800d37`.

## Source-pinned comparison before mutation

The profiles were generated from clean `git archive` snapshots, not from this
working tree. The cross-package command was:

```text
CGO_ENABLED=0 go test ./... -tags=nomicrophone -timeout=480s -coverpkg=github.com/portpowered/go-agent-harness/agent-cli/...,github.com/portpowered/go-agent-harness/go-agent-runtime/... -coverprofile=<snapshot>/agent-cli/full.cover.out
```

Accepted main (`4a1c399ccbb3d780be95eb04316e84b8f11a6646`) exited 0. Raw profile
was `/tmp/audio-runtime-c156-profiles.zQtIoK/accepted-main.full.cover.out`,
SHA-256 `7dcb067aeba346493fe957425be51039cd2ad24fc9f72f477b1b8303d869929b`.
`go tool cover -func` reported overall 70.0%; the deduplicated
`agent-cli/internal/services/agentsession` profile contained 76 statements,
63 covered, 82.89%.

C145 head (`d014a3586368c37e20618481ee162e1b83db113f`) produced a profile but
the command exited 1 because unrelated shared-host integration fixtures failed
(`TestRunRoom_BoundGraceExpiryCancelsActiveResponseCleanly`, virtual audio
device setup, and replay/provider-negative fixtures). The failed command is
retained as failed evidence, not labeled green. Raw profile was
`/tmp/audio-runtime-c156-profiles.zQtIoK/c145-head.full.cover.out`, SHA-256
`f8c3f2e6c1d451ee1033920a360f62771182f8e3ae93dd25ad5d6741a4ea39b5`.
`go tool cover -func` still reported overall 70.1%; the deduplicated
agentsession profile contained 76 statements, 47 covered, 61.84%, reproducing
the CI-reported 61.80% floor failure.

Relevant exact `go tool cover -func` rows before the repair were:

```text
interface.go: NewSessionCancellationIntent 100.0%, MarkSIGINT 100.0%, SIGINTReceived 100.0%, RecordSessionDiagnostic 100.0%, RecordSessionToolDiagnostic 100.0%
tool_lifecycle.go: sentinel Error 0.0%; constructor 87.5% (main) / 0.0% (C145); unresolved Error 84.6% / 69.2%; Unwrap 100.0%; UnresolvedCallIDs 66.7% / 66.7%
validation.go: duration Error 0.0% / 0.0%; duration Unwrap 100.0%; ValidateSessionMaxDuration 100.0%; barge Error 66.7%; barge Unwrap 100.0%; ValidateSessionAudioInTurnBarge 80.0%; reasoning 100.0%
voices.go: Error 66.7%; Unwrap 66.7%; SupportedOpenAIRealtimeVoices 100.0%; ValidateOpenAIRealtimeVoice 100.0%
```

The exact uncovered executable locations were attributed before test
mutation:

- Tool sentinel/typed-error and constructor normalization: `tool_lifecycle.go:18.59,18.79; 31.140,34.25 (3 statements, C145 only); 34.25,36.15 (2, C145 only); 36.15,37.12; 39.3,39.36 (C145 only); 39.36,40.12; 42.3,43.32 (2, C145 only); 45.2,48.29 (3, C145 only); 48.29,49.37 (C145 only); 49.37,51.4 (C145 only); 53.2,53.90 (C145 only)`.
- Tool lifecycle outcomes and ownership: `tool_lifecycle.go:57.14,59.3; 61.19,63.3; 68.59,70.4 (C145 only); 72.26,74.3 (C145 only); 84.14,86.3`. These are nil-safe/empty errors, non-empty status formatting, and owned ID-copy outcomes.
- Duration and barge typed errors: `validation.go:24.50,25.14; 25.14,27.3; 28.2,28.79; 54.14,56.3; 71.19,73.3`. These are nil-safe error text, negative-duration creation, barge nil-safe text, and negative-count clamping.
- Voice typed errors: `voices.go:39.14,41.3; 50.14,52.3`. These are nil-safe error text and nil `Unwrap` behavior.

The new tests exercise those public outcomes through exact values, typed
`errors.Is`/`errors.As`, deterministic ordering, copy isolation, callback
records, and caller-owned buffer/metadata fields. They do not import a concrete
provider or device and do not perform device I/O.

The source-pinned package slices retained in this evidence directory are
`accepted-main.agentsession.cover.out` (SHA-256
`2c577b997f3d3e9558d3d8bdfa133e39f62142ae8cc08a79ea013952fac24cb1`, exact
`go tool cover -func` total 82.9%) and `c145-head.agentsession.cover.out`
(SHA-256 `2abc1111016b434e44ec01d72273685ae63fb0af91347f80605f2931b432f3a3`,
exact total 61.8%). They contain the deduplicated agentsession entries from
the raw all-package profiles; the raw profile SHA-256s above bind them to the
source-pinned commands.

## Candidate verification record

The candidate package profile is written to `candidate.cover.out` beside this
file by the exact C156 command:

```text
rtk proxy sh -c 'cd agent-cli && CGO_ENABLED=0 go test ./internal/services/agentsession -tags=nomicrophone -count=1 -coverprofile=../docs/temp/projects/audio-runtime/audio-runtime-c156-repair-agentsession-coverage-regression/candidate.cover.out -timeout=120s'
```

It exited 0 and reported 100.0%. The profile SHA-256 is
`f494ac5dc338340d18dd9a2c4f4229b90a44b6e1f94034c45f1316583ab29029`.
The exact filtered `go tool cover -func` result was:

```text
interface.go: NewSessionCancellationIntent 100.0%; MarkSIGINT 100.0%; SIGINTReceived 100.0%; RecordSessionDiagnostic 100.0%; RecordSessionToolDiagnostic 100.0%
tool_lifecycle.go: sentinel Error 100.0%; NewSessionUnresolvedToolResultsError 100.0%; unresolved Error 100.0%; Unwrap 100.0%; UnresolvedCallIDs 100.0%
validation.go: duration Error 100.0%; duration Unwrap 100.0%; ValidateSessionMaxDuration 100.0%; barge Error 100.0%; barge Unwrap 100.0%; ValidateSessionAudioInTurnBarge 100.0%; reasoning 100.0%
voices.go: Error 100.0%; Unwrap 100.0%; SupportedOpenAIRealtimeVoices 100.0%; ValidateOpenAIRealtimeVoice 100.0%
total: (statements) 100.0%
```

The pre-main-update full `make coverage` gate exited 0. Its full profile
`coverage/agent-cli.out` SHA-256 is
`fc780f0f90263ed708d667b2acebd9b0a6cd8bf8a9eab8085348d4c50946c83e`; the
deduplicated agentsession slice is 76/76 statements, 100.00%, with the
manifest still exactly:

```text
{"package": "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession", "minimum": 80.00}
```

Pre-main-update focused and accumulated results (all exit 0):

- `rtk proxy sh -c 'cd agent-cli && CGO_ENABLED=0 go test ./internal/services/agentsession -tags=nomicrophone -run C156 -count=50 -timeout=180s'` — output SHA-256 `0295a4010a0b99cece5ee92fa70d62590e19a62e5468d3461a9db9666ed281b8`.
- `rtk proxy sh -c 'cd agent-cli && CGO_ENABLED=0 go test -race ./internal/services/agentsession -tags=nomicrophone -run C156 -count=20 -timeout=300s'` — output SHA-256 `305b1ce180e65bad40e397e71c87d1adb2c994b0ef98f7980b99d5e52cb999e3`.
- Full package normal 10× and race 5× commands from C156-03 — output SHA-256s `64c549fe7215236aa5be893b79c7fb15c9f7de7cc6a9c65d2953bb23242ee670` and `7993295fee4c3394219925ca3e7a9a91e8d235e7a7a83f825169b7ab8755230e`.
- `TestSessionCommand_ActiveScheduledAudioPreservesToolResultLifecycle` — exit 0 in 4.458s; output SHA-256 `915e9709bf66c001034d157b98622b76c873b4da51f70bc35c5955552d4a663f`.

The first broad `make coverage` attempt is retained as a failed environmental
run, not as a candidate pass: exit 2 after 5m18.543s, with the agentsession
package itself passing and unrelated `internal/services/internal/agentruntime`
fixtures failing. Exact failures included
`TestRunRoom_BoundGraceExpiryCancelsActiveResponseCleanly/duration` (expected
one cancellation, got zero), replay mismatch/provider-negative fixtures, and
virtual audio-device setup errors. Its captured output SHA-256 is
`7899403245aa2dc21d56e7c36aeb28142a7a29273b6048173c7db5192c12fa5e`. The
second same-command rerun passed; its captured output SHA-256 is
`50775893e240d7b364ec1162979ed916553d2d25ca8a9a45a93d0b838f070ae3`.

After fetching and merging review-time `origin/main` `97d3dcfb1e97a2611aa26b203a7f893442db4768`
(isolated merge commit `37f773eea`), the final-head reruns also all exited 0:

- Focused C156 normal 50× output SHA-256: `0920c9440d326364061e087c9db08aaad126b04741ddf146699166c2e4be2a8d`.
- Focused C156 race 20× output SHA-256: `e289e85a642781c8579d2ca2423e75166521a4816223e9a2f9a38c79a3454758`.
- Full agentsession normal 10× and race 5× output SHA-256s: `64c549fe7215236aa5be893b79c7fb15c9f7de7cc6a9c65d2953bb23242ee670` and `4b2d56f74b5ea26cb0745a9f26619d5273e90e6ff3373620e96a2caac600f0d9`.
- Accumulated lifecycle regression output SHA-256: `b6543c13602de9139a2296761e3d1882e3a5c7180539d9507033697cc2900998`.
- Final merged-head `make coverage` output SHA-256: `38b09267937154f9507382d3de71a98f2fe5032b77b768a0ce8132683549e519`; full profile SHA-256 `a743c8c00682fc2b40ed8f58108803e9b30eb902dc93256ec31275e18ccc554f`, with agentsession 76/76 (100.00%).

## SCRIPT CI handoff

- Implementation checkpoint commit: `fa598fca7ac70ae531d56d68d4626d9c96a52816`.
- Review-time main integration: merged fetched `origin/main` `97d3dcfb1e97a2611aa26b203a7f893442db4768` into the isolated candidate; its merge base with the candidate is the planning main `4a1c399ccbb3d780be95eb04316e84b8f11a6646`.
- Review-time merge commit: `37f773eea` (the final evidence checkpoint is a descendant of this merge).
- Pushed branch: `codex/audio-runtime-c156-repair-agentsession-coverage-regression`.
- Pull request opened: `https://github.com/portpowered/go-agent-harness/pull/517`.
- The executor stops here after submitting the exact pushed candidate to SCRIPT
  CI. CI owns current-head polling and any rejection returns to this same task;
  no CI status is inferred or claimed by this evidence.

## Rejected static head and bounded repair

- The full raw log for PR 517's rejected `CI (static)` job
  `103800583639` from run `34785669810` was retrieved at the completed job
  boundary; its raw-job metadata, capture provenance and extracted findings
  are preserved in `ci-rejection-34785669810.json`. The raw 938-line log
  SHA-256 is
  `791524d1c71c3a69315fa7c18ed497fb3eabe2eb917133217720cb1fec8c909b`.
  There was no C156 review row; this was a CI rejection of the pushed head
  `aab19728058b5783a279aa2301681ab7da976585`.
- The complete log identifies only owned-test findings: cognitive complexity
  `25 > 20` in `TestC156AudioInTurnBargeContract`, `21 > 20` in
  `TestC156VoiceContractIsOrderedAndCopyIsolated`, three `errorlint` direct
  error comparisons, and one `goconst` repeated `"mutated"` literal. No
  production, coverage-manifest, threshold, exclusion, or C145 path was
  implicated.
- The bounded repair keeps the public behavior assertions intact, factors
  barge and invalid-voice checks into small assertion helpers, uses
  `errors.Is` for wrapped-error identity, and names the shared mutation value
  as a test constant. No production behavior or acceptance threshold changed.
- Post-repair validation before the evidence update: focused C156 normal
  `-count=50` and race `-count=20` pass; full agentsession normal `-count=10`
  and race `-count=5` pass; the candidate package profile is `76/76`
  statements (`100.0%`) with SHA-256
  `f494ac5dc338340d18dd9a2c4f4229b90a44b6e1f94034c45f1316583ab29029`; the
  seven-profile coverage gate passes with `192` registered packages; the
  accumulated `TestSessionCommand_ActiveScheduledAudioPreservesToolResultLifecycle`
  regression passes; architecture-size, Wire, vet, lint (`0 issues`) and
  Staticcheck pass. The full `make coverage` run generated all seven profiles,
  and the exact coverage gate was rechecked without rerunning tests.
