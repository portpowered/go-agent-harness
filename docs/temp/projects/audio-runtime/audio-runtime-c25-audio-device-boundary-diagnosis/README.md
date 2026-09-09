# C25 audio/device boundary diagnosis

This is the admitted `audio-runtime-c25-audio-device-boundary-diagnosis` evidence folder. It is intentionally source-only: no production, shared-module, fixture, baseline, device, provider, realtime, or acoustic changes are part of this task.

## Finding

The smallest concrete remaining bypass is the compiled legacy CLI room implementation:

- `agent-cli/internal/room/mixer.go` owns a host `time.Ticker` and local PCM16 encoding/decoding.
- `agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go` constructs that mixer and opens `go-device-gateway` source/sink handles.
- `agent-cli/internal/services/internal/agentruntime/session_room_run.go` resamples/decodes pending PCM and calls `sink.WriteFrame` directly.

The current public `yui room run` command is wired through `go-agent-runtime/services/rooms` (`agent-cli/internal/wire/wire_gen.go:109-110` and `agent-cli/internal/transport/cli/room.go:52`), so this packet does not claim that the legacy path is the active public workflow. The old `RunRoom` path is compiled and exercised by its own internal `agentruntime` package tests, but `agent-cli/internal/services/servicetest/runtime.go:109-112` exports only session helpers and does not expose `RunRoom`. That is a concrete source/ownership gap and migration hazard, not runtime/acoustic proof.

## Verification

Run from the repository root:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c25-audio-device-boundary-diagnosis/verify.py --mode all
```

The verifier pins freshly fetched main revision `98ce636dd67349ba64f22cd7916dd370cf4ba484` separately from the docs candidate, reproducibly hashes a `git archive` of that pin, verifies the fixture manifest and every audited production path, enforces a complete source-to-candidate changed-path allowlist, enforces a 55-second child cap and 600-second aggregate cap, terminates timed-out process groups, records source SHA-256 values, checks the public/canonical package graph, rejects a negative bypass fixture, and runs only these focused checks:

- canonical `go-audio/pkg/mixer` mixer/accumulator tests;
- canonical playback queue/callback-boundary tests;
- canonical deterministic-clock boundary tests;
- canonical room lifecycle graph/media-bridge tests;
- the go-agent-loop production ownership guard;
- one legacy mixer behavior test;
- one legacy agent-runtime behavior test, its race run, and focused vet.

The predecessor pushed candidate-head run completed in 10.325 seconds; its
evidence remains preserved below. Every child is bounded to 55 seconds and the
aggregate to 600 seconds with process-group cleanup.

The exact current-head refresh at `209afd4ceca417fdca7964e3c0301ad4d3d1c250`
completed in 10.263 seconds with 19 commands, no unexpected failure, clean
source/path checks, and no surviving child processes. The generated
`diagnosis.json` and `provenance.json` are stamped with that candidate; the
earlier ff00c037, dab5c373, 3c2aea55, and 10.325-second runs remain preserved as
predecessor checkpoints.

After merging fetched `origin/main` at `98ce636dd67349ba64f22cd7916dd370cf4ba484`,
the exact candidate refresh at `88f996c5384222d97b92578a6ba7f1ea83adbf2e`
completed in 9.475 seconds with 19 bounded commands. It reported
`SOURCE_GAP_CONFIRMED`, accepted source ancestry and audited-path equality,
accepted every focused package/control check, and recorded the intentional
1-second child-hang control as an expected terminated failure with no surviving
children. The refreshed source archive is `e77933bdc0f47a2f885581eb9b51a297b763f7a7b54bffe3b07254fa2126f11e`.

The precommit Review-78 repair rerun at source candidate
`8e96dc077744d204994274b22356af8e856a8aa7` completed in 16.275 seconds with
23 bounded commands and no unexpected failure. It records the exact
reproducible source archive (`e77933bdc0f47a2f885581eb9b51a297b763f7a7b54bffe3b07254fa2126f11e`),
the committed fixture manifest and per-fixture digests, a complete changed-path
allowlist with no outside paths, fail-closed dependency controls, and explicit
zero-test discovery handling. The earlier `209afd4c`, `67265932`, and other
hashes remain preserved as predecessor checkpoints.

The committed repair-head rerun at source candidate
`9e61354c27ca381ed204ab05dec2aa7fd816c1d9` completed in 10.503 seconds with
23 bounded commands and no unexpected failure. Its 14 committed changed paths
and empty working-tree path set were fully allowlisted; `diagnosis.json`,
`diagnosis.md`, and `provenance.json` identify this measured candidate. The
final evidence-only successor remains confined to the same owned folder.

The current-head CI rejection was separately characterized read-only. Run `34397773527` / job `102621627290` failed only in the hosted integration matrix at `test46/provider_burst`: after all provider responses and callback-clock shutdown, the full `/v1/audio-device/control/snapshot` evidence request exceeded the existing 30-second scenario context at `session_tool_audio_remote_e2e_test.go:182`. The production-binary device replay and committed regression suite in that job passed. The exact production/fixture owner is C20; C25 makes no repair outside this evidence folder. Local single and full-matrix focused runs passed, so this remains an intermittent hosted diagnosis rather than a claimed fix.

C20's reviewed recording/live repair has not reached `origin/main` in this refresh;
the timeout remains C20-owned historical evidence. C25 therefore imports no C20
production or fixture changes and makes no CI-green claim.

## Review-86 causal repair checkpoint

At committed verifier repair source candidate
`6151f4b7eede2a8b5a5fd9867c856a8743ddc412`, `verify.py --mode all` completed in
14.734 seconds with decision `ACCEPTED`. The verifier now uses a parsed Go AST
dependency oracle instead of fixture-text token checks: the canonical fixture is
accepted, the forbidden fixture is rejected, an allowed fixture mutated with a
`time.NewTicker` edge is rejected, and the same forbidden tokens in comments are
ignored. The public-route reachability fields are derived from the inspected
source, not hard-coded.

The fresh run retained 55-second child and 600-second aggregate bounds, passed
the cold-cache zero-test control after bounded compilation allowance, preserved
the pinned archive/ancestry/path checks, passed all focused normal/race/vet
regressions, and left no child process. This repair remains source/package
evidence only; the hosted C20 `test46/provider_burst` snapshot timeout remains
historical, separately owned, and unwaived.

## Current-head CI rejection checkpoint

At the pushed candidate `b061bf852076d4b36526600377ad916751e32b4b`, PR417's
latest script-CI coverage job (`34413969081` / `102674534021`) failed only at
`agent-cli/internal/wire/composition_test.go:417`: the composed session error
was nil where the test expected the RTC media-capability error after preflight.
The full 31,872-byte job log is hashed in [`ci-diagnosis.json`](ci-diagnosis.json)
and the raw job metadata is retained in the owned folder. The focused local
reproduction, including coverage instrumentation, passed in `0.563s` with
`4.7%` package coverage. This is an external composition regression, not a
C25 verifier failure; C25 has no lease for the production or composition-test
path, and makes no CI-green claim.

The exact next action is for the existing C20 owner to repair and review that
composition assertion/implementation on the same project task. After the
reviewed repair reaches `origin/main`, C25 will refresh its exact-head
evidence and resubmit the changed same task through the script CI gate. No
unchanged implementation is being resubmitted.

## Exact-head verifier refresh after rejection

At local checkpoint `a52291203d244d9d4f4d3a06511b9519ac65acc9`, the bounded
`verify.py --mode all` command returned `ACCEPTED` in 14.0 seconds across 25
commands. The pinned source/archive, AST positive/negative dependency controls,
fixture digests, changed-path allowlist, focused normal/race/vet checks, zero-test
and child-hang controls all remained accepted, with no surviving child process.
This is an owned evidence refresh only; it does not repair the C20 composition
failure or authorize an unchanged CI resubmission.

## Extraction plan

The single chosen dependency is the legacy room-owned PCM16 cadence/mixer boundary. The exact current/proposed paths, APIs, callers, ownership, wire sequence, trigger, executable regression, and AUDIO/DEVICE/SERVICE/QUALITY gate map are in [`extraction-plan.md`](extraction-plan.md) and [`extraction-plan.json`](extraction-plan.json).

Accepted C15/C16/C17 evidence proves software replay, software sink lifecycle, and canonical PONG-clock behavior only. Physical playback, microphone capture, live provider behavior, and acoustics remain unproved.
