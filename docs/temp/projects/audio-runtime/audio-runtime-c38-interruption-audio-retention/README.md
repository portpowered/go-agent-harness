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
