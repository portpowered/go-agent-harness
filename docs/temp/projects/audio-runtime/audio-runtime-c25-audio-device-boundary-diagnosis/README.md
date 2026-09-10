# C25 audio/device boundary diagnosis

This is the admitted `audio-runtime-c25-audio-device-boundary-diagnosis` evidence folder. It is intentionally source-only: no production, shared-module, fixture, baseline, device, provider, realtime, or acoustic changes are part of this task.

## Finding

After re-evaluating the legacy mixer against accepted C26, the smallest
concrete remaining bypass is the compiled legacy CLI room clock/format/device
output boundary:

- `agent-cli/internal/room/mixer.go` owns a host `time.Ticker` and PCM16
  format adaptation around the canonical mixer. C26 moved the old accumulation
  and final clipping operation into `go-audio/pkg/mixer.MixPCM16Samples`; no
  legacy local accumulation or clipping loop remains to diagnose here.
- `agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go` constructs that mixer and opens `go-device-gateway` source/sink handles.
- `agent-cli/internal/services/internal/agentruntime/session_room_run.go` resamples/decodes pending PCM and calls `sink.WriteFrame` directly at `:1186`.

The current public `yui room run` command is wired through `go-agent-runtime/services/rooms` (`agent-cli/internal/wire/wire_gen.go:109-110` and `agent-cli/internal/transport/cli/room.go:52`), so this packet does not claim that the legacy path is the active public workflow. The old `RunRoom` path is compiled and exercised by its own internal `agentruntime` package tests, but `agent-cli/internal/services/servicetest/runtime.go:109-112` exports only session helpers and does not expose `RunRoom`. That is a concrete source/ownership gap and migration hazard, not runtime/acoustic proof.

## Verification

Run from the repository root:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c25-audio-device-boundary-diagnosis/verify.py --mode all
```

The verifier pins freshly fetched main revision `c95a2cb4f96fa8c14bd4655f5197a822c86a980c` separately from the docs candidate, reproducibly hashes a `git archive` of that pin, verifies the fixture manifest and every audited production path, confirms accepted C26 merge ancestry, enforces a complete source-to-candidate changed-path allowlist, enforces a 55-second child cap and 600-second aggregate cap, terminates timed-out process groups, records source SHA-256 values, checks the public/canonical package graph, rejects a negative bypass fixture, and runs only these focused checks:

- canonical `go-audio/pkg/mixer` mixer/accumulator tests;
- canonical playback queue/callback-boundary tests;
- canonical deterministic-clock boundary tests;
- canonical room lifecycle graph/media-bridge tests;
- the go-agent-loop production ownership guard;
- the accepted C26 shared-DSP reconciliation tests;
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

## Current-main refresh after reviewed C21 merge

The fetched `origin/main` advanced to reviewed PR414 merge commit
`a1156f0c0c6271643578cb37894026944df2a633` (all nine required checks passed).
It is a C21 device-consumption merge, not the missing C20 composition repair.
That main was merged into the isolated C25 branch at candidate
`39f7b6a8b454adb170b010ac280c65d96354278f`, preserving the C21 ancestry and
leaving the C25 change set confined to its admitted evidence folder relative
to current main.

The verifier now pins `a1156f0c0c6271643578cb37894026944df2a633` and its reproducible
55,592,960-byte archive SHA-256
`0e8f182d3c58853213cf575e2d7b6e0a125b92fab96ac3c67969b8ab409c841f`.
At measured candidate `39f7b6a8b454adb170b010ac280c65d96354278f`,
`verify.py --mode all` returned `ACCEPTED` in 10.4 seconds across 25 bounded
commands: source-gap, archive, fixture, AST dependency, allowlist, focused
normal/race/vet, zero-test, timeout-cleanup, and aggregate-shutdown controls
all passed with no surviving child process.

This remains source/package evidence only. PR417 is still rejected at
`b061bf852076d4b36526600377ad916751e32b4b` by the C20-owned coverage failure
at `agent-cli/internal/wire/composition_test.go:417`; PR412 has not put a
reviewed C20 repair on main. C25 will retain the task, avoid an unchanged
submission, and refresh again only after that same-task C20 repair is reviewed
into main.

## Extraction plan

The single chosen dependency is the legacy room-owned PCM16 cadence/mixer boundary. The exact current/proposed paths, APIs, callers, ownership, wire sequence, trigger, executable regression, and AUDIO/DEVICE/SERVICE/QUALITY gate map are in [`extraction-plan.md`](extraction-plan.md) and [`extraction-plan.json`](extraction-plan.json).

Accepted C15/C16/C17 evidence proves software replay, software sink lifecycle, and canonical PONG-clock behavior only. Physical playback, microphone capture, live provider behavior, and acoustics remain unproved.

## Current-head verifier refresh

At measured candidate `70f2496f16b8bf036cbe654d99a121ac0a1346f7`, the bounded
`verify.py --mode all` command returned `ACCEPTED` in `14.582s` across `25`
commands. Source-gap, ancestry/path, archive, fixture, AST dependency,
focused normal/race/vet, zero-test, timeout-cleanup and aggregate-shutdown
controls passed with no surviving child process. The source archive remained
`55,592,960` bytes with SHA-256
`0e8f182d3c58853213cf575e2d7b6e0a125b92fab96ac3c67969b8ab409c841f`.

This refresh changes only the owned evidence checkpoint. The C20-owned
`composition_test.go:417` coverage rejection at `b061bf85` remains unresolved
on `origin/main`; C25 has no authorized repair path and will not resubmit an
unchanged implementation. After reviewed C20 repair reaches main, refresh the
exact source evidence and submit the changed same task through script CI.

## Current resumed handoff

Fetched `origin/main` is `431fc96c14f0e0045629d9c36f98ee61ff06e840`; the
isolated branch integrates it at `2574dc27bef53828ac4503047b3d6036ec68cc50`.
The required baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` and startup
integration `8bdafc7f947a3a2c9856220abdc539437035bd21` remain ancestors. The
C20 composition repair merge `b0acab1238d1aa6bf6bce5ca074451310c7eb039` is
now on main, and its former focused assertion passes locally. The separate
C20 `test46/provider_burst` hosted timeout remains historical, unwaived, and
separately owned.

The accepted C26 merge `1f82284abee0bd31a6680310444cea2e4c16ef00` is also an
ancestor. Its shared-DSP causal regression passed three tests; source
inspection finds the shared bounds/mix calls at `mixer.go:725` and `:751` and
no legacy accumulation or final-clipping loop. The remaining
`SOURCE_GAP_CONFIRMED` is the legacy host ticker plus format adaptation and
direct `DeviceSink.WriteFrame` boundary at `session_room_run.go:1186`.

At measured candidate `2574dc27bef53828ac4503047b3d6036ec68cc50`, the bounded
verifier returned `ACCEPTED` in `15.518s` across `27` commands, including `10`
focused normal/race/vet regressions. Archive, fixture, AST dependency,
changed-path allowlist, zero-test, timeout-cleanup, and aggregate-shutdown
controls passed; no child survived. The current archive is `56,913,920` bytes
with SHA-256
`e6f17306b3baf55d44511108871c9ec0546d0c5e3a4639deafc05dd85abf4c31`.

The exact next action is to commit and push this evidence-only checkpoint on
the existing task branch, update PR417 with the same-head evidence, and return
the changed task to the script CI gate without polling CI. Any exact CI
rejection remains actionable on this same task; C25 retains no production,
shared-module, baseline, device, provider, realtime, or acoustic ownership.

## Exact-head verifier refresh at the resumed candidate

At measured candidate `0a623e78f5d56aa75e5438995da57c1a5c891fe7`, the bounded
`verify.py --mode all` command returned `ACCEPTED` in `17.849s` across `25`
commands. `diagnosis.json` and `provenance.json` now identify this exact
candidate; source-gap, archive, fixture, AST positive/negative/mutation,
complete allowlist, focused normal/race/vet, zero-test, child-hang and clean
shutdown controls all passed with no surviving child process. The source
revision remains `a1156f0c0c6271643578cb37894026944df2a633`, with archive
SHA-256 `0e8f182d3c58853213cf575e2d7b6e0a125b92fab96ac3c67969b8ab409c841f`.

This is an owned evidence-only checkpoint. The latest hosted rejection remains
the C20-owned `composition_test.go:417` coverage failure at `b061bf85`; no
reviewed C20 repair is present in fetched `origin/main`, so C25 will not submit
an unchanged candidate to script CI. The evidence checkpoint must be pushed
and PR417 updated; after the reviewed C20 repair reaches main, refresh again
against that changed source and submit the same task through the script gate.

## Current exact-head refresh after current-main integration

All checkpoints below this section are historical and remain preserved for review.
The fetched reviewed `origin/main=c95a2cb4f96fa8c14bd4655f5197a822c86a980c` was
merged into the isolated C25 branch at `b957dd96224f76a4919960413d4a6dcbc1dc6bb1`;
the verifier was rerun at tested source candidate
`8c61326167ceb6ed2017b46ff866656d99a711b7`. The remaining metadata commit is
an evidence-only descendant of that tested source; no executable inputs differ.
Required baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21`, and accepted C26 merge
`1f82284abee0bd31a6680310444cea2e4c16ef00` remain ancestors.

At tested source `8c61326167ceb6ed2017b46ff866656d99a711b7`, `verify.py --mode all`
returned `ACCEPTED` in `16.671` seconds across `27` bounded commands, including `10`
focused normal/race/vet regressions. `SOURCE_GAP_CONFIRMED`, the AST positive,
negative, mutation and comment controls, fixture manifest, rebuilt/materialized
archive, complete path allowlist, zero-test and child-hang cleanup controls all
passed; no child survived. The pinned archive is `57,067,520` bytes with SHA-256
`5e8cb07b1bc2ef3b999ca57458100bb6620697a0af481c28436493f8a3737a12`.

The focused current-main composition regression also passed with coverage in
`0.555` seconds (`4.5%` statements). The historical PR417 coverage rejection
(`34413969081`/`102674534021`, `composition_test.go:417`) remains preserved and
C20-owned; C20's separate `test46/provider_burst` timeout remains unwaived.
No hosted CI result, merge, runtime, physical-device, acoustic, or project
acceptance is claimed. The synchronized evidence-only descendant is pushed to
PR417. Next action: submit this changed same task to the script CI gate without
polling; retain ownership for any exact rejection.

## Exact current-head refresh

At pushed `HEAD` `50cab0a75732f7d669d8d0ec232a1f854db30368`, the bounded
`verify.py --mode all` run returned `ACCEPTED` in `11.975` seconds across 25
commands. `SOURCE_GAP_CONFIRMED`, source/archive/fixture/AST/allowlist checks,
focused normal/race/vet regressions, zero-test discovery, child-hang cleanup,
and aggregate shutdown all passed with no surviving child. The generated
`diagnosis.json` and `provenance.json`, ownership checkpoint, extraction plan,
and CI evidence now identify this exact head. This remains source/package
evidence only; the C20-owned coverage rejection is unchanged and no CI-green,
merge, runtime, physical-device, acoustic, or project-acceptance claim is made.
