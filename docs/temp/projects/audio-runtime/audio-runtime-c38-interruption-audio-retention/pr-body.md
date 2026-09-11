# C38 interruption audio retention — executor handoff

Project: `audio-runtime` (`audio-runtime-v1`)

This is the existing admitted task and existing PR #430. The isolated branch is
`codex/audio-runtime-c38-interruption-audio-retention`; `prd.json.branchName`
matches it. The candidate preserves startup, baseline, C30 planning, and current
`origin/main=aa31d6d0e7261f7dcfc07e8f14a7ad29501f542b` ancestry through merge
`01c8a8410f3a98d082f9285301fda3429d34602e`. No second
project, acceptance waiver, host-checkout reset, or predecessor mutation was
used.

## Review and CI repair map

- Reviews 219, 229, and 249: provider-terminal barrier, bounded quiet drain,
  delayed-delta/canceled-connect controls, and deterministic interrupted-prefix
  retention in the leased session audio-output path.
- Review 245: queued-after-cancellation messages use bounded non-cancellable
  retention before `WriteSamples`, with a deterministic queued-delta regression.
- Review 258: malformed-delta and sink-write paths join and report provider
  close errors, with sentinel tests.
- Prior CI run 34539207051 / job 103077775076 (`CI (static)`) found the sole
  errcheck at `session_tool_audio_remote_oracle_test.go:32` for discarded
  `sink.Close()`. The deferred cleanup now reports close failure through the
  test. The repair is in checkpoint `83e31db1`.

## Evidence

- C38 session-audio normal and race focused suites: 20 tests each; vet,
  architecture-size (`184` packages / `1,888` files / `27,806` functions),
  size-check, Wire, pinned golangci-lint v2.9.0, staticcheck 2026.1, and
  diff-check pass.
- The newly authorized remote-marker causal control passes normally. Its race
  build was blocked before test execution because macOS `strip` could not write
  the helper with only about 493 MiB free; the required 2 GiB reserve is not
  available. This is not reported as a product pass or failure.
- Existing C38 exact artifact evidence remains historical and is not relabeled:
  the merged main and new integration test changed executable/build inputs.
  A fresh yui build, repaired replay, package provenance, and remote-control
  race run must be produced from the final clean head after storage recovery.

No script-CI success, independent review, guarded merge, post-delivery vertical
acceptance, physical/acoustic proof, or project completion is claimed here.
The next action is to restore stable free space above 2 GiB, run the remaining
bounded C38 causal/original/repaired/negative/cleanup/focused/package controls
from the clean head, then submit this same PR to the script-owned CI gate
without polling. Retain the task for any exact rejection or actionable repair.

## Fresh exact-head handoff — 2026-09-10T23:18Z

The storage prerequisite was available for this run: approximately 5.9 GiB was
free before the build and approximately 3.5 GiB remained afterward, above the
required 2 GiB reserve. The clean tested source is
`5d2e029a51e934fb8dcc78702ad8acb14c5e4402`.

- `repaired-20260910T231538Z-77788` returns `REPAIRED_ORACLE_PASS`. The newly
  built yui is 50,912,034 bytes,
  `8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`.
  Tool PCM is 4,800/3,200 bytes with the frozen hashes; interruption PCM is
  3,840/3,360 bytes with the frozen hashes and 2,400-byte healthy tail; strict
  replays pass.
- `causal-20260910T231514Z-76987` passes the deterministic retention barrier.
  `focused-checks-20260910T231349Z-72475` passes normal/race, vet,
  architecture/size (`184` packages / `1,888` files / `27,806` functions),
  and Wire. The remote-marker control passes normal and race.
- `negative-controls-20260910T231525Z-77395` passes the 21-case C30 consumer
  plus exit-1 negative control, real same-length PCM/hash mutation rejection,
  and missing-timeline rejection. `cleanup-control-20260910T231528Z-77578`
  passes capped output, TERM/KILL, reaping, and no survivors.
- `original-20260910T231759Z-79359` returns
  `HISTORICAL_FAILURE_PRESERVED` against the immutable C30 artifacts and
  records the live legacy pass as scheduling variation only. `package` returns
  `PACKAGE_READY` for the exact source/artifact/build inputs; build-input SHA is
  `7689716a955acc5299b34c23aa3d600fa6c7829eed577c93232b2b0984716309` over
  `2,185` inputs and the architecture helper SHA is
  `debe7ca096d60db699974b7d9a37ba15d146ec3f26a860f45544f96a9e6d547f`.

The candidate is ready for the script-owned current-head CI gate. This does not
claim CI success, independent review, guarded merge, vertical acceptance, or
project completion. Submit the changed same-task head once without polling;
retain C38 ownership through `CONTINUE` for any exact rejection or actionable
repair.

## Pushed-head provenance confirmation — 2026-09-10T23:23Z

The exact pushed head is `8bce982045d81696348188b1e28194d005c25a8d`.
`repaired-20260910T232125Z-80573` returns `REPAIRED_ORACLE_PASS` and
`package` returns `PACKAGE_READY` for that exact source identity. The new yui
artifact remains 50,912,034 bytes with SHA-256
`8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`; the
frozen tool/interruption PCM and strict replay results remain exact. The
build-input manifest is unchanged at SHA-256
`7689716a955acc5299b34c23aa3d600fa6c7829eed577c93232b2b0984716309` over
`2,185` inputs. The remote-marker control passes at this head in normal and
race modes.

This confirms executor readiness for the script-owned current-head CI gate; it
does not claim CI success, independent review, guarded merge, vertical
acceptance, or project completion.

## Current-main integration and latest CI rejection — 2026-09-10T23:54Z

Fresh exact-head `repaired-20260910T234538Z-94510` returns
`REPAIRED_ORACLE_PASS`; `package` returns `PACKAGE_READY` for merge head
`01c8a8410f3a98d082f9285301fda3429d34602e`. The yui artifact is 50,912,034
bytes with SHA-256
`8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`; the
build-input manifest is SHA-256
`7689716a955acc5299b34c23aa3d600fa6c7829eed577c93232b2b0984716309` over
2,185 inputs. Exact-head causal/focused checks and remote-marker normal/race
controls pass.

The latest script-CI rejection is run `34541949856` / job `103086327724` at
`bea84fd9`: only `CI (coverage)=FAILURE`. Its completed log reports
`s2s_room_input_transcription_test.go:141`, where participant `alpha` emitted
`[session.update input_audio_buffer.append]` rather than one initial
`session.update`. The exact test passes 5/5 locally, and that source is outside
the C38 owned paths. Do not mutate or retry that unchanged out-of-lease
finding; route it to its owner for a reviewed repair or disposition. This PR
update claims no CI success, independent review, guarded merge, vertical
acceptance, physical/acoustic proof, or project completion.

## Full coverage-log reconciliation and bounded revalidation — 2026-09-11T00:10Z

The complete coverage job log was read rather than relying on the board summary.
It contains the out-of-lease room handshake failure above and the C38-leased
`TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`
failure at `session_tool_audio_remote_e2e_test.go:183`. That run recorded
`rendered_pcm=471360` versus `expected_pcm=174391`, no device drops/overflows,
all 9 provider responses and 7 tool results, and a child still running while
the callback clock underflowed before the final marker.

The C38 failure is not reproduced by bounded exact controls: the provider-burst
case passed five times in 69.079s, and the concurrent test45/test46 provider-
burst pair passed three times in 45.901s. The owned session-output suite passes
normal/race 20/20, remote oracle controls pass normal/race 29/29, and the exact
current-head causal runner and negative controls pass. Existing deadlines,
fixtures, assertions, and source are unchanged; no C38 source repair is
justified by this scheduling-only observation. The room failure remains outside
the C38 lease and requires primary routing to its owner. No CI-green, review,
merge, vertical-acceptance, physical, or project-complete claim is made.

## Same-task room ordering repair and final executor handoff

The current implementation handoff supersedes the older out-of-lease note:
C38 now owns the exact room test for this bounded repair. Current
`origin/main=7f73c8b3b4ebc99b55b8bb5e802beff024385407` is integrated through
merge `c30a6c47bd63d83ddd0f289f6086b8a0cb955dbd`, preserving the required
startup, baseline, and C30 planning ancestors.

The repair is in
`agent-cli/internal/services/internal/agentruntime/s2s_room_input_transcription_test.go`.
The fake provider withholds response completion until it receives media. A
manual mixer cadence advances every participant only after both actual
`SESSION.OPEN` observations, deterministically proving the CI-observed
post-handshake sequence `[session.update, input_audio_buffer.append]` without
sleep/retry or a production ordering change. The validator requires exactly one
initial `session.update`, allows only subsequent `input_audio_buffer.append`
media, and rejects duplicate/out-of-order handshakes, unexpected controls, and
missing media. The stale architecture exception for the now-compliant test was
removed; no gate threshold was lowered.

Evidence at source `0629bc5c033d57d578bc5187f33f2c35f985e552`:

- room ordering normal/race: 7 tests each;
- accumulated session-output normal/race: 19 tests each;
- owned remote integration cases: 14 tests;
- causal: `CAUSAL_PROOF` at `causal-20260911T010855Z-43952`;
- focused: `FOCUSED_CHECKS_PASS` at `focused-checks-20260911T011346Z-48409`,
  including vet, architecture/size (184 packages, 1,888 files, 27,819
  functions), and Wire;
- negative and cleanup controls pass at
  `negative-controls-20260911T011605Z-50072` and
  `cleanup-control-20260911T011610Z-50122`;
- repaired replay: `REPAIRED_ORACLE_PASS` at
  `repaired-20260911T011513Z-49294`; package manifest: `PACKAGE_READY`;
- rebuilt yui: 50,912,034 bytes,
  SHA-256 `8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`;
- clean 2,185-input build manifest SHA-256
  `155a2d6d1258a30a6596d91bab06e52059bbdc2d3a1c922e8cd06f7665775950`.

Frozen tool/interruption PCM and healthy-tail hashes, strict replay,
transcript/timeline/terminal evidence, same-length PCM mutation rejection, and
missing-timeline rejection all pass. This is executor handoff only: it does not
claim script CI success, independent review, guarded merge, post-delivery
vertical acceptance, physical/acoustic proof, or project completion. The next
action is to evaluate this genuinely changed same-task head in script-owned
current-head CI without polling; retain C38 ownership for any exact rejection
or actionable repair.

## Pinned static finding repair and current-head handoff

The exact latest rejection was run `34550421782` / job `103111974625` at prior
head `31f8ede4`; only pinned `golangci-lint` failed, reporting `goconst` for
the two room-test literals at lines 248 and 260. Commit
`6113661d536158be9cebe751344913eee1f2c8fd` replaces those literals and the
remaining matching test values with the existing canonical event constants.
No wire behavior, fixture, oracle, timeout, baseline limit, or production
ownership changed.

Exact-head evidence:

- room handshake/order normal and race: 35/35 each;
- accumulated session-output normal and race: 240/240 each;
- owned remote provider-burst/slow-device normal and race: 15/15 each;
- `make lint`: all 15 modules, 0 issues;
- causal/focused/negative/cleanup runner records:
  `causal-20260911T013757Z-58519`, `focused-checks-20260911T013810Z-58584`,
  `negative-controls-20260911T013851Z-59057`, and
  `cleanup-control-20260911T013856Z-59088`;
- fresh `REPAIRED_ORACLE_PASS`: `repaired-20260911T013717Z-58407`, yui
  SHA-256 `8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`;
- `PACKAGE_READY`: 2,185-input build manifest SHA-256
  `54fc81f532dc47cb3505b48b77e25e9646f767d067c9a6e706397395cf86fb41`.

Frozen PCM/healthy-tail, strict replay, real same-length PCM mutation,
missing-timeline, and bounded cleanup controls pass. This remains executor
handoff evidence, not CI-green, review, merge, vertical, physical/acoustic, or
project acceptance. The next action is to push this same task and return
`ACCEPTED` to script-owned current-head CI without polling; retain C38 through
`CONTINUE` for any exact rejection or actionable repair.

## Clean provider-close retention candidate — 2026-09-11T03:48Z

`c51ccb78528fe7513df265ee357c327cb2fe38ec` repairs teardown ordering so
accepted audio is drained under the bounded retention context before provider
close; `b13c7aa5bdb1848fea4d7acca1aac277f6dc4935` consolidates its deterministic
cancellation-barrier regression to the unchanged 684-line test baseline.

At `b13c7aa`, the focused C38 normal/race gates pass 19 tests each, targeted
vet is clean, `make size-check` passes 184 packages / 1,888 files / 27,817
functions, and the accumulated review regressions pass in normal/race
`COUNT=3` modes. Clean causal evidence is
`evidence-b13c7aa/runs/causal-20260911T034525Z-25422`: `CAUSAL_PROOF`, exact
source identity `b13c7aa5bdb1848fea4d7acca1aac277f6dc4935`, regression exit 0
in 6.834s, reaped parent, and no survivors. Frozen wire/PCM/rendered/healthy-
tail, strict replay, mutation, and missing-timeline controls remain proven.

The broad package race retains only the known unrelated timing failures in
`TestBrowserConversationCommandValidatorReadsBoundedStructuredVerdict` and
`TestSessionDynamicToolPublisher_CoalescesSelectionCatalogBurst`; no
out-of-scope repair was made. This is executor handoff evidence, not CI-green,
review, merge, vertical, physical/acoustic, or project acceptance. Next action:
push `b13c7aa`, update PR `#430`, and return `ACCEPTED` to script-owned
current-head CI without polling.

## Pinned static rejection repair — 2026-09-11T04:33Z

The canonical board rejection at head `23a9a168` was CI run
`34559981128`, job `103140529372`, `CI (static)`. Its completed log had only
five `errcheck` findings, all in the owned
`session_audio_out_test.go` cancellation-barrier fixture: sink creation,
inferencer connect, session close, the old type assertion, and provider close.

Commit `aa1a3dc3e812df1be9ad5b311dc4bc5154334dd1` checks each result through a
test-only helper, uses the already-typed output session channel, and uses the
embedded provider's `Done` signal for the pre-drain close assertion. The
fixture remains exactly 684 lines, and no production behavior, wire fixture,
oracle, deadline, baseline, or ownership boundary changed.

At `aa1a3dc`, pinned `make lint` reports 0 issues in all 15 modules;
architecture-size, diff-check, focused normal/race (22/22 each), and vet pass.
The accumulated `COUNT=3` session regression script passes normal, coverage,
and race modes. The exact remote continuation matrix passes 13/13 normally;
its race run passes the prior `test46/slow_device` target and only exposes the
known concurrent `long_prior_input_61s` stress cases. Isolated `test46/slow_device`
and the remote-marker oracle each pass in normal and race.

Clean exact-source runner records are `causal-20260911T043350Z-50563`
(`CAUSAL_PROOF`, 8.861s, source `aa1a3dc`) and
`focused-checks-20260911T043350Z-50564` (`FOCUSED_CHECKS_PASS`, normal/race,
vet, architecture/size at 184/1,888/27,817, and Wire). The bounded
negative-control and cleanup records are
`negative-controls-20260911T042725Z-46601` and
`cleanup-control-20260911T042725Z-46602`.

This remains executor handoff evidence only: no current-head script-CI
success, independent review, guarded merge, post-delivery vertical
validation, physical/acoustic proof, or project acceptance is claimed. The
next action is fresh exact-source artifact replay/package provenance, then
push/update PR `#430` and return `ACCEPTED` to script-owned current-head CI
without polling; retain C38 ownership for any exact rejection.
