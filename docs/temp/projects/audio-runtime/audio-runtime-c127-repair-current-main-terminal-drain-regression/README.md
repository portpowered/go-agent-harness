# C127 current-main terminal-drain regression

This evidence belongs to the admitted `audio-runtime` / `audio-runtime-v1`
task `audio-runtime-c127-repair-current-main-terminal-drain-regression`.
The implementation candidate is source checkpoint `bb88393e41ff93d3f1663b4085c8b03ac11baf6b`
on branch
`codex/audio-runtime-c127-repair-current-main-terminal-drain-regression`.
The isolated worktree is the worktree containing this file. The immutable
current-main negative control is pinned at
`09c70f51243caeaf1184c4806b99bbf7749e3044`; a fresh fetch resolved
`origin/main` to `bd6a1289218d1bef1a3af36e64e9d4496062416f`, which is merged
into the candidate at `edd209d66`. Startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21` and accepted C64 merge
`59af6325614d80173447fe2018a0471e27b4e7b1` are ancestors of the candidate.
C118 remains preserved at clean PR 504 head
`4d5e0801c9df7c4f33a2498d0a20acf947b03992`.

## Causal result

The recurrence is before sink admission. The live terminal-drain wrapper
connected the provider and called `Receive()` before the later
`capturingInferencer` claimed provider RTC media. The OpenAI session prepares
unclaimed media for the provider read loop, then `Receive()` calls
`releaseUnclaimedRTCMedia()`, which closes that media. The first provider audio
block can therefore be received while the local RTC endpoint is still
unclaimed. The immutable PR 504 records show the resulting one-block loss:
`171191/177591` compared samples, exactly `6400` missing, with zero dropped,
overflow, discarded, or discard-event counters and a zero terminal queue.

The repair claims the provider media immediately after `ConnectSession()` and
before `Receive()` in `service.go:352-366`. It uses the existing
`captureMediaEndpoints()` helper in `start_support.go:337-345`, preserving the
`OutputAudioContinuous` option. The later adapter claim is idempotent against
the already-claimed provider endpoint. No PCM format, queue capacity, timeout,
drain policy, cancellation identity, interruption behavior, or assertion was
weakened.

The paired source regression is recorded in
`causal/terminal_drain_test.go:119-241`: a provider fake records an explicit
six-boundary sequence with monotonic-process timing and 6,400-sample ranges at
the public live-service boundary. On unmodified current main the temporary
causal test fails with
`provider media admitted during Receive: context deadline exceeded`; after the
repair the candidate records
`rtc_forwarding -> provider_receipt -> sink_admission -> device_render ->
response_terminal -> graceful_drain`. The complete bounded comparison is in
`causal-run.json`: candidate and accepted C64 pass, unmodified pinned current
main fails at the RTC preclaim boundary, every child is reaped, and the
aggregate run is within 600 seconds.

## Immutable controls and prior findings

The complete prior PR 504 metadata and failed logs are retained beside this
README as `ci-run-34748383831.*` and `ci-run-34749714271.*`. Their SHA256
digests and exact failure observations are in `causal-evidence.json`.
The accepted C64 source-first failure and repaired control remain unchanged at
`../audio-runtime-c64-provider-audio-terminal-drain-repair/negative-evidence.json`.

There was no prior C127 review finding. The latest C118 review finding
(scheduled incomplete/continuation metadata being ignored by terminal
finalization) belongs to C118's disjoint terminal-diagnostics paths; those
paths and its checkpoint are unchanged. C51 review-27 and review-35 findings
(boundary/provenance/build-manifest and process-group/quotas evidence) likewise
belong to the separate C51 slice; this evidence records exact code citations,
source/build identity, bounded process observations, and cleanup status without
claiming those slices repaired. No C98 digest artifact exists in this checkout;
the accepted C64 evidence is the available immutable control and is not
relabeled as C98.

## Verification

The final post-edit checks were:

* `go test ./go-agent-runtime/services/session/internal/live -count=1 -timeout=180s`
* `go test ./go-agent-runtime/services/session/internal/live/causal -run '^TestTerminalDrainOrderedBoundaryTrace$' -count=1 -timeout=60s`
* `go test -race ./go-agent-runtime/services/session/internal/live/causal -run '^TestTerminalDrainOrderedBoundaryTrace$' -count=3 -timeout=120s`
* `go test -race ./go-agent-runtime/services/session/internal/live -run 'BindPlaybackController|Terminal|Drain|Close|Cancel|Tool|CapturingInferencer' -count=3 -timeout=180s`
* focused RTC tests in `agent-cli/internal/services/internal/agentruntime` and
  `go-device-gateway/pkg/runtime`
* `make vet`, `make lint` (pinned golangci-lint 2.9.0), `make staticcheck`
  (pinned staticcheck 2026.1), `make wire-check`, `make architecture-check`,
  and `make coverage-registration`
* separate, unchanged, strict `TestAgentBinaryTest45HighRateToolAudioRegression`
  and `TestAgentBinaryTest46HighRateToolAudioRegression` runs
* accumulated normal, coverage, and race session regression harnesses, all
  exiting zero

The focused and accumulated checks exited zero. Coverage registration exited
zero; the full changed-package coverage attempt reached the unrelated
`TestShippedSessionSIGINTDuringToolExecutionFinalizesCleanly` deadline failure
under broad instrumentation. That exact test passes on both fetched
`origin/main` and the candidate outside the broad coverage run, and its source
is outside the C127 lease, so the failure remains executor-owned rather than a
C127 product result. The exact commands, exits, and output summaries are in
`verification-summary.json`.

The subsequent SCRIPT CI run `34759672383` also rejected the candidate: static
reported the then-root causal test's 772-line file and its 26/28
cyclomatic/cognitive complexity, while coverage failed the out-of-lease
`TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab`.
The static finding is repaired by moving the causal probe into
`live/causal/terminal_drain_test.go` (241 lines) and splitting its helpers; the
parent live package remains within its 15-file budget. The browser failure
remains preserved as an external ownership finding, not a C127 runtime result;
the exact browser test passes in a standalone local run.

The source-pinned `nomicrophone` YUI build from the candidate has SHA256
`8e8db1f19527d10cc7ea53653db95a790f6852199784efe10e011be5f238ab1d`
(`51,101,938` bytes). The bounded public runner and the offline replay from
that binary exited zero and emitted `PROBE_TOOL_MARKER_9182`, an ordered tool
call/result, continuation text, provider-close terminal metadata, and 4,800
bytes of output PCM with terminal queue zero. Artifact hashes are recorded in
`provenance.json`, `verification-summary.json`, and `public-run.json`. The
strict test45/test46 checks provide the separate software-device/tool process
boundary proof. This remains software-device/offline replay evidence only; no
credentials, live Realtime session, physical device, or acoustic claim was
used.

The bounded runner recorded process-group cleanup for every causal and public
child. A final target-process check for `audio-device-server` was empty after
the strict integration cleanup. This is recorded as harness cleanup evidence,
not as a product pass. Script CI has not been polled and this candidate does
not claim green CI or project-wide completion.
