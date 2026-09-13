# C127 current-main terminal-drain regression

This evidence belongs to the admitted `audio-runtime` / `audio-runtime-v1`
task `audio-runtime-c127-repair-current-main-terminal-drain-regression`.
The implementation candidate is commit `54277fe562c79c28a8a452c090293dfa41698b48`
on branch
`codex/audio-runtime-c127-repair-current-main-terminal-drain-regression`.
The isolated worktree is the worktree containing this file. The source base was
freshly fetched `origin/main` at
`09c70f51243caeaf1184c4806b99bbf7749e3044`; startup integration
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
`service_test.go:571-600`: a provider fake records whether media was claimed
when the terminal-drain connection is established. On unmodified current main
the temporary causal test failed with `read provider media admitted during
Receive: context deadline exceeded`; after the repair the claim-order check,
focused package tests, and strict production-binary regressions pass.

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
* `go test -race ./go-agent-runtime/services/session/internal/live -run 'BindPlaybackController|Terminal|Drain|Close|Cancel|Tool|CapturingInferencer' -count=3 -timeout=180s`
* focused RTC tests in `agent-cli/internal/services/internal/agentruntime` and
  `go-device-gateway/pkg/runtime`
* `go vet` for all three relevant packages
* `make architecture-check` and `make size-check`
* separate, unchanged, strict `TestAgentBinaryTest45HighRateToolAudioRegression`
  and `TestAgentBinaryTest46HighRateToolAudioRegression` runs

All exited zero. The exact commands, exits, and output summaries are in
`verification-summary.json`.

The source-pinned `nomicrophone` YUI build has SHA256
`dabf6c52683d84409c5b7d31380254d6b17b7d8d72586c18bf622622901afdf0`.
The offline public replay from that binary exited zero and emitted
`PROBE_TOOL_MARKER_9182`, an ordered tool call/result, continuation text,
provider-close terminal metadata, and 4800 bytes of output PCM. Artifact
hashes are recorded in `provenance.json` and the replay manifest. This is
software-device/offline replay evidence only; no credentials, live Realtime
session, physical device, or acoustic claim was used.

The cleanup audit found three exact `audio-device-server` processes left by an
earlier aborted characterization attempt; they were terminated by exact PID
and the final target-process check was empty. This is recorded as harness
cleanup evidence, not as a product pass. Script CI has not been polled and
this candidate does not claim green CI or project-wide completion.
