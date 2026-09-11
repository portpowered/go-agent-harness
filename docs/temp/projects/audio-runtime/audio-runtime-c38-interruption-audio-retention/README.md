# C38 interruption audio-retention evidence

This is the admitted `audio-runtime` task
`audio-runtime-c38-interruption-audio-retention`. It preserves the failed C30
result, proves the cancellation/retention boundary, repairs only the leased
session audio-output path, and packages a bounded runner for independent
current-head validation.

## Admission and ancestry

- branch: `codex/audio-runtime-c38-interruption-audio-retention`
- source plan: `factory/projects/audio-runtime/source-plan.md#governing-execution-plan`
- required startup ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- required baseline ancestor: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`
- C30 planning source: `2525da44053e5bfe7e2d8fccc55463a645107e29`
- fresh `origin/main` is integrated in the candidate before handoff

The original C30 report and failure decision remain read-only references under
`$FACTORY_ROOT/docs/temp/projects/audio-runtime/`. The staged C30 yui and
consumer are also immutable references and are verified by SHA-256 on every
`original` and `negative-controls` run:

- yui: `01864224f0257ad7d934401053e091d40019e74faad37dd69638e3041b7ae446`
- consumer: `53c2ff160c262da04ea153a81ba5b0b10f20cc8a66011d50d3c887a1819993d9`

The exact-source architecture/Wire helper supplement is packaged as
`architecture-tools.tar.gz`, SHA-256
`debe7ca096d60db699974b7d9a37ba15d146ec3f26a860f45544f96a9e6d547f`, with its
source descriptor in `helper-inputs.json`.

## Frozen inputs and oracles

The private fixture copies are byte-identical to C21:

- `fixtures/c16-audio-tool.session.json`: SHA-256
  `38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169`
- `fixtures/c16-interruption.session.json`: SHA-256
  `154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`

The tool fixture requires 18 wire events, one real `exec` call, marker
`PROBE_TOOL_MARKER_9182`, provider PCM 4,800 bytes with SHA-256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`, and
rendered PCM 3,200 bytes with SHA-256
`7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`.

The interruption fixture requires 15 wire events, no tools, provider PCM
3,840 bytes with SHA-256
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`, and
rendered PCM 3,360 bytes with SHA-256
`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`. The
healthy replacement tail is 2,400 bytes with SHA-256
`16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`; the
runner checks it at provider offset 1,440 and rendered offset 960. A clean
child exit, suffix match, or queue admission alone never satisfies these
oracles.

## Causal repair

The archived C30 source-2525 run is retained as `REPLAY=FAIL`: it recorded only
the healthy 2,400-byte replacement at both PCM boundaries. A live run of the
immutable old executable is performed once; if scheduling produces a full
oracle pass, it is recorded as nondeterministic variation and cannot replace
the archived failure.

The owned repair is confined to
`session_audio_out.go` and `session_audio_out_test.go`:

- provider ingress is connected without the caller cancellation signal;
- the wrapper waits for the underlying provider terminal signal (or its bounded
  wall-time fallback), requests the underlying session close, joins the close
  completion/recording relay, and drains the finite accepted provider buffer
  under a bounded, non-cancellable teardown context;
- assistant PCM is written before best-effort public-buffer publication during
  teardown; and
- clean interruption uses barrier regressions for both a delayed delta and a
  cancellation that races session connection, then accepts and verifies the
  healthy replacement in order.

The regression preserves explicit cancellation, malformed-delta and sink-write
error behavior. It does not preserve arbitrary stale messages: only accepted
assistant audio deltas are drained. The shared architecture baseline entries
for the owned production file and its duration function were lowered only after
the implementation was reduced below the prior measured values; no gate limit
was raised.

## Bounded runner

All modes preserve argv, environment, stdout/stderr, exit status, hashes,
wire/timeline order, artifacts, and process-group cleanup in a timestamped
`runs/` directory. Child commands are bounded to 60 seconds, targeted checks to
120 seconds, and the aggregate public reproduction contract to 600 seconds.

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/run.py --mode original
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/run.py --mode causal
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/run.py --mode repaired --artifact <new-exact-source-yui> --fixtures <frozen-C21-copies>
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/run.py --mode repaired --build-artifact <new-artifact-path> --fixtures <frozen-C21-copies>
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/run.py --mode negative-controls
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/run.py --mode cleanup-control
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/run.py --mode focused-checks --source-root <candidate-source>
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/run.py --mode package --artifact <new-exact-source-yui>
```

`original` runs the exact 21-case consumer and both public replay commands
against immutable artifacts, including strict directory replay. `causal` checks
the source boundary and deterministic barrier regression. `repaired` rebuilds
when no artifact is supplied, then requires every frozen PCM, trace, manifest,
transcript, session-log, terminal, marker, and strict-replay oracle. The
negative suite mutates a real rendered PCM byte and proves the validator
rejects its changed SHA-256, while strict replay rejects a missing timeline. The
cleanup control verifies bounded TERM/KILL/reap with capped output and no
surviving process group.

## Handoff limits

The evidence is credential-free software/file replay evidence. It makes no
Realtime, physical-device, microphone, speaker, acoustic, or CI-green claim.
The next external steps are the script-owned current-head CI gate, independent
Luna review, guarded merge, and fresh post-delivery vertical validation. CI is
not polled by this runner.

## Fresh exact-head validation

The storage prerequisite was restored before this validation: about 5.9 GiB was
free before the build and about 3.5 GiB remained afterward, above the required
2 GiB reserve. The clean tested source is `5d2e029a51e934fb8dcc78702ad8acb14c5e4402`.

- `repaired-20260910T231538Z-77788` built a new yui artifact at that exact
  source. The artifact is 50,912,034 bytes with SHA-256
  `8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`.
  The tool capture is 4,800/3,200 bytes with the frozen provider/rendered
  hashes, and interruption is 3,840/3,360 bytes with the frozen hashes and
  the 2,400-byte healthy tail. Both strict bundle replays pass.
- `causal-20260910T231514Z-76987` passes the deterministic cancellation barrier
  and identifies the owned `session_audio_output_session.forward` boundary.
  `focused-checks-20260910T231349Z-72475` passes normal/race, vet,
  architecture/size, and Wire checks (`184` packages, `1,888` files,
  `27,806` functions).
- `negative-controls-20260910T231525Z-77395` preserves the 21-case C30
  consumer and exit-1 control, rejects a real same-length PCM/hash mutation,
  and rejects missing timeline. `cleanup-control-20260910T231528Z-77578`
  passes capped output, TERM/KILL, reap, and no-survivor checks.
- The newly leased `TestRemoteToolAudioSlowDeviceEdgeOracleControl` passes in
  both normal and race modes with the existing final-marker and zero-loss
  oracle. `original-20260910T231759Z-79359` preserves the immutable C30
  `HISTORICAL_FAILURE_PRESERVED` record; its live legacy pass is retained as
  scheduling variation only.
- `package` passes with build-input manifest SHA-256
  `7689716a955acc5299b34c23aa3d600fa6c7829eed577c93232b2b0984716309` over
  `2,185` inputs and architecture-helper SHA-256
  `debe7ca096d60db699974b7d9a37ba15d146ec3f26a860f45544f96a9e6d547f`.

These are executor handoff results only. Script CI, independent review,
guarded merge, and the primary's exact-artifact vertical probe remain open.

## Pushed-head provenance confirmation

After the handoff ledger commit, the exact pushed head
`8bce982045d81696348188b1e28194d005c25a8d` was rebuilt and packaged. Run
`repaired-20260910T232125Z-80573` returns `REPAIRED_ORACLE_PASS` with the same
artifact SHA-256 and frozen PCM results, and `package` returns
`PACKAGE_READY` for that exact source identity. The build-input manifest remains
`7689716a955acc5299b34c23aa3d600fa6c7829eed577c93232b2b0984716309` over
`2,185` inputs; the only delta from the prior tested source was the tracked
handoff documentation/progress ledger.

The remote-marker control also passes in normal and race modes at this pushed
head. No CI, review, merge, vertical acceptance, or project-completion result
is implied.

## Current-main integration and latest CI rejection

The current main baseline `aa31d6d0e7261f7dcfc07e8f14a7ad29501f542b` was
fetched and merged into exact C38 head `01c8a8410f3a98d082f9285301fda3429d34602e`;
the shared `progress.txt` conflict was resolved by retaining both append-only
ledgers. Fresh exact-head `repaired-20260910T234538Z-94510` returns
`REPAIRED_ORACLE_PASS`, and `package` returns `PACKAGE_READY`. The artifact is
50,912,034 bytes with SHA-256
`8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`; the
2,185-input build manifest SHA-256 is
`7689716a955acc5299b34c23aa3d600fa6c7829eed577c93232b2b0984716309`.

Exact-head causal and focused checks pass, as do the normal/race remote-marker
controls. The latest CI rejection is run `34541949856` / job `103086327724` at
`bea84fd9`: `CI (coverage)=FAILURE` from
`s2s_room_input_transcription_test.go:141`, where participant `alpha` emitted
`[session.update input_audio_buffer.append]` instead of one initial
`session.update`. The test passes 5/5 locally, and that file is outside C38's
owned paths; it must be routed to its owner rather than changed in this task.
No CI-green, review, merge, vertical-acceptance, physical, or project-complete
claim is made.

## Room handshake-order repair and final candidate

The current implementation handoff supersedes the older out-of-lease note
above: C38 now owns the exact room test for this bounded repair. Current
`origin/main=7f73c8b3b4ebc99b55b8bb5e802beff024385407` is integrated through
merge `c30a6c47bd63d83ddd0f289f6086b8a0cb955dbd`; startup, baseline, and C30
planning ancestry remain present.

The repair is in
`agent-cli/internal/services/internal/agentruntime/s2s_room_input_transcription_test.go`.
The fake provider withholds response completion until media arrives, and a
manual mixer cadence advances only after both actual `SESSION.OPEN`
observations. This deterministically proves the legitimate post-handshake wire
sequence `[session.update, input_audio_buffer.append]` without sleep/retry or a
production ordering change. The strict validator permits exactly one initial
`session.update` and only subsequent `input_audio_buffer.append` media;
duplicate/out-of-order handshakes, unexpected controls, and missing media are
negative controls. The stale exception for this now-compliant test was removed
without lowering any architecture gate threshold.

At source `0629bc5c033d57d578bc5187f33f2c35f985e552`, room ordering normal/race
both pass 7 tests. The accumulated C38 session-output suite passes normal/race
19 tests each; the two owned remote integration cases pass 14 tests; causal,
focused, negative-control, and cleanup runner modes pass. Focused checks cover
vet, architecture/size (`184` packages, `1,888` files, `27,819` functions), and
Wire.

Fresh exact replay at
`runs/repaired-20260911T011513Z-49294` is `REPAIRED_ORACLE_PASS`; the rebuilt
artifact is 50,912,034 bytes with SHA-256
`8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`.
`package-manifest.json` is `PACKAGE_READY`; the clean 2,185-input manifest is
SHA-256
`155a2d6d1258a30a6596d91bab06e52059bbdc2d3a1c922e8cd06f7665775950`.
Frozen tool/interruption PCM and healthy-tail hashes, strict replay, transcript,
timeline, terminal, same-length PCM mutation, and missing-timeline controls
all pass. These remain executor handoff results only; script current-head CI,
independent review, guarded merge, and fresh post-delivery vertical validation
are still external gates.

## Pinned static finding repair and exact-head handoff

CI run `34550421782`, job `103111974625`, failed at prior head `31f8ede4` only
on two `goconst` findings in the newly leased room test. Commit
`6113661d536158be9cebe751344913eee1f2c8fd` reuses the existing canonical
`sessionUpdateEventType` and `inputAudioBufferAppendEventType` constants; wire
behavior and all assertions are unchanged.

Post-repair room controls pass normal/race 35/35 each; accumulated session
output passes 240/240 each; and the owned remote controls pass 15/15 each.
Pinned `make lint` reports 0 issues in all 15 modules. Focused causal,
architecture/size (184/1,888/27,819), Wire, vet, negative-control, and cleanup
checks pass in exact-head runner records
`causal-20260911T013757Z-58519`, `focused-checks-20260911T013810Z-58584`,
`negative-controls-20260911T013851Z-59057`, and
`cleanup-control-20260911T013856Z-59088`.

Fresh exact-source `repaired-20260911T013717Z-58407` is
`REPAIRED_ORACLE_PASS`; its rebuilt yui is 50,912,034 bytes with SHA-256
`8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`, and
`PACKAGE_READY` passes for the clean 2,185-input source with build-input SHA
`54fc81f532dc47cb3505b48b77e25e9646f767d067c9a6e706397395cf86fb41`.
Frozen PCM, healthy-tail, strict replay, real same-length mutation rejection,
missing-timeline rejection, and bounded TERM/KILL cleanup remain proven.

This is executor handoff evidence only. The next action is to update PR #430
with this changed head and return `ACCEPTED` to script-owned current-head CI
without polling; CI, independent review, guarded merge, and post-delivery
vertical validation remain external gates.

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

## Fresh exact-source replay/package — 2026-09-11T04:37Z

From clean source `596d8e9a96b12d32e073a97f07552d4e236dfd20`,
`repaired-20260911T043648Z-51985` returns `REPAIRED_ORACLE_PASS`. The new yui
artifact is 50,895,362 bytes with SHA-256
`79eb1eb02e813a9b427164a4938199f03c18c430b51b99c791ed2a8af20ec831`.
Tool replay remains 18 wire events/1 tool call with 4,800 provider and 3,200
rendered bytes and the frozen hashes; interruption remains 15/0 with 3,840
provider and 3,360 rendered bytes, including the exact 2,400-byte healthy tail.
Both strict replays pass, and the missing-timeline control rejects as expected.

`package-20260911T043706Z-52052` returns `PACKAGE_READY` for the clean branch,
with startup, baseline, planning, and fresh `origin/main=7f73c8b3` ancestry
verified. The build manifest covers 2,185 inputs with SHA-256
`b3ff27ac38d91183097cf162de890e0486e1804c0ec5e2d7f7b66a4ac9fe2fc4`;
`repaired-build.json` is SHA-256
`c1907f1259986fc10f659baa641fc690ad030e7178c6e9a31543b3d77e49e6b6`, and
`package-manifest.json` is SHA-256
`e194eef647aa183665840bf35d0711adc56ca09dc7ad5c125573cfe3298bd723`.

The candidate is now ready to push and hand to the script-owned current-head
CI gate. This does not claim CI green, independent review, guarded merge,
post-delivery vertical validation, physical/acoustic proof, or project
acceptance.

## Final exact-head artifact/package replay — 2026-09-11T04:38Z

The clean executable candidate at `4bd514e6f1980532a331e0f51b3d66fde08eeecb`
produced `REPAIRED_ORACLE_PASS` in
`repaired-20260911T043825Z-52250`. The rebuilt yui is `50,895,362` bytes with
SHA-256 `79eb1eb02e813a9b427164a4938199f03c18c430b51b99c791ed2a8af20ec831`.
Tool replay remains 18 wire events/1 tool call with 4,800 provider and 3,200
rendered bytes; interruption remains 15/0 with 3,840 provider and 3,360
rendered bytes, including the exact 2,400-byte healthy tail. Frozen hashes,
strict replay, same-length mutation rejection, and missing-timeline rejection
all remain proven.

`package-20260911T043835Z-52326` returns `PACKAGE_READY` for that clean exact
source. Its build covers 2,185 inputs with SHA-256
`b3ff27ac38d91183097cf162de890e0486e1804c0ec5e2d7f7b66a4ac9fe2fc4`, and the
artifact hash/size match the repaired replay. Startup, baseline, planning, and
fresh `origin/main=7f73c8b3b4ebc99b55b8bb5e802beff024385407` ancestry are
verified. The final ledger commit containing this section is documentation-
only; no executable build input or oracle changed after the exact replay.

This remains executor handoff evidence only. The next action is to push/update
PR `#430` and return `ACCEPTED` to script-owned current-head CI without
polling; CI, independent review, guarded merge, post-delivery vertical
validation, physical/acoustic proof, and project acceptance remain external
gates.
