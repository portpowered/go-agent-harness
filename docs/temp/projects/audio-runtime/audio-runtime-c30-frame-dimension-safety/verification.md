# C30 integrated-main repair and exact-source evidence — 2026-09-10

The admitted branch integrated the freshly fetched `origin/main` at
`c95a2cb4f96fa8c14bd4655f5197a822c86a980c` in merge commit
`a87247bf77abaf6746820c3eb2073d33166c3ac1`. The startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21` and baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` remain ancestors. The host checkout
was not merged or reset; `meta-operator-throughput-feedback.md` remains an
untracked user file. `format.go` and `pcm16_convert_test.go` remain unchanged
against integrated `origin/main`, while C30 owns the consolidated framing files.

The implementation and accumulated causal controls pass on the integrated tree:

```text
go test ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s: PASS, 293 tests
go test -race ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s: PASS, 293 tests
go vet ./go-audio/pkg/audio ./agent-cli/internal/room: PASS
make architecture-check size-check: PASS, 181 packages / 1866 files / 27532 functions
git diff --check: PASS
```

The bounded exported consumer was rebuilt from `a87247bf` and its manifest pins
the source root, source module, executable SHA256
`1f61b0c03879e8a871fc492eee18de9d2126012519911c8c800504175c4b8ca4`, and exact
fixture hashes. Positive literal/range/error controls pass; the mutated-480
negative control exits 1 with `actual_samples=480 mutated_expected=481`; the
output-flooding timeout control caps both 1 MiB pipes, sends TERM/KILL, reaps the
child after KILL, stops readers, and completes in under five seconds.

The same-source shipped `yui` artifact is pinned by
`artifacts/yui-verification.json`: executable SHA256
`963720da43bbd04e26192edffb2a2048374eb0fba064b4a6da1adcbbb566b7f6`, 1,927
tracked Go/module inputs with aggregate SHA256
`71f8b6896fcf6412c6b4318b45b44577f08b366659d4d3fb15f3bd2548197167`, and the
current C21 fixture hashes. Tool capture/replay passes with 18 wire events and
one tool call; interruption capture/replay passes with 15 wire events and zero
tool calls. Provider PCM is 4,800 bytes with SHA256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502` for the
tool fixture and 3,840 bytes with SHA256
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22` for the
interruption fixture. The no-trace strict replay negative control rejects the
bundle for missing `timeline.jsonl`. The first fresh tool run's missing
`evidence/runs` setup failure is preserved in `runs/current-a872-tool-trace/failure.json`;
the corrected v2 run is the passing result.

All executable artifacts above were tested at `a87247bf`; the final evidence
commit is explicitly an evidence-only descendant. The Go/module input count and
aggregate hash, source root, fixture hashes, and executable hashes are the
identity proof that adding reports did not change executable inputs. No current
head CI success, independent review, guarded merge, post-merge vertical
acceptance, physical/acoustic proof, or project completion is claimed here.

# C30 CI rejection reconciliation — evidence-only descendant

The prior implementation source remains `a7302e076768d730cce0cfac7997be6b4fc84969`.
The current branch head before this evidence checkpoint is
`6eb1f72182f6debace03c6315b5cd513f3a7e176`; the only changes after the tested
implementation are within this owned evidence directory, so these artifacts are
not relabeled as a build from the documentation descendant.

The complete raw CI rejection payload for run `34442412086` was saved at
`/tmp/audio-runtime-c30-ci-rejection-34442412086.json` (SHA256
`10a27e0f43d8909815f45cf2d8d60a3886b1aa6eaa4bb4c571639ff234b8a9e6`, 16,111
bytes). The complete `CI (hermetic)` job log was saved at
`/tmp/audio-runtime-c30-ci-hermetic-34442412086.log` (SHA256
`559f0ffdda3632c5fb124a23d161f45744b98768cc37bbf0e911bf1cdf050714`, 296
lines, 34,361 bytes) and read in full. The run checked out merge ref
`522202ed915924b295e48e3f1b65f44056125f33`, merging PR #422 head
`6eb1f72182f6debace03c6315b5cd513f3a7e176` into `b0acab1238d1aa6bf6bce5ca074451310c7eb039`.

All required jobs passed except `CI (hermetic)`. Its sole failing package
control was the untouched, out-of-lease
`agent-cli/internal/transport/cli/internal/events` test:

```text
TestRoomUsesLiveTimeoutForPeerFilteredEventsAndEvidence
room_live_liveness_test.go:40: room did not publish liveness through the live service
```

The current C30 diff has no path under that package. The exact test passes in
the isolated C30 worktree in both relevant modes:

```text
go test ./agent-cli/internal/transport/cli/internal/events -run '^TestRoomUsesLiveTimeoutForPeerFilteredEventsAndEvidence$' -count=5 -timeout 60s: exit 0, 5/5
CGO_ENABLED=0 go test ./agent-cli/internal/transport/cli/internal/events -tags=nomicrophone -run '^TestRoomUsesLiveTimeoutForPeerFilteredEventsAndEvidence$' -count=5 -timeout 60s: exit 0, 5/5
```

This is a nonreproduced external-package CI failure, not a C30 source defect;
no out-of-lease repair or acceptance waiver was made. Review132's Stats and
constructor findings, Review139's owned-path/provenance finding, and Review149's
current-main and bounded-runner findings remain covered by the source and
evidence sections below. The C30 source diff still contains only the owned
framing/room paths and this evidence directory; `format.go` and
`pcm16_convert_test.go` remain unchanged.

The evidence-only ancestry check is explicit: `git diff --name-only
a7302e076768d730cce0cfac7997be6b4fc84969 HEAD` contains only files under
`docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety`.
The tested consumer source revision, executable SHA256, fixture hashes, yui
source revision/input digest and replay hashes remain the values recorded for
`a7302e07`; no executable input changed after that implementation checkpoint.

No current-head CI success, independent review, guarded merge, post-merge
vertical acceptance or project completion is claimed. After this checkpoint is
committed and pushed, the same PR is ready for script-owned CI resubmission;
the prior hermetic failure must not be treated as a C30 repair or as green CI.

# C30 merged-main repair and exact-head verification

Implementation source revision: `a7302e076768d730cce0cfac7997be6b4fc84969`
(`fix: bound c30 consumer process cleanup`). Its parent merge
`60f0d3073b09c043d95c3fb3e17b489261abc5d2` integrates fetched `origin/main`
`b0acab1238d1aa6bf6bce5ca074451310c7eb039` into the admitted branch
`codex/audio-runtime-c30-frame-dimension-safety`. The startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21` and baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` remain ancestors. The running host
checkout was not merged or reset. `format.go` and `pcm16_convert_test.go` are
byte-identical to `origin/main`.

The amended C30 lease consolidates the unchanged PCM16 framer implementation
and test into the owned frame-sizing source/test files, preserving all framer
symbols and assertions while keeping the audio package at its immutable file
budget. Checked canonical sizing still handles exact alignment, rate-duration
intermediates, channel products, byte products, MaxInt boundaries and wide
`Stats` duration arithmetic; legacy methods preserve `ErrMixerInvalidFormat`.

## Causal and accumulated checks

```text
focused PCM16 frame/framer/dimension/queue/Stats tests: pass; 22 tests across 2 packages
go test ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s: pass; 285 tests
go test -race ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s: pass; 285 tests
go vet ./go-audio/pkg/audio ./agent-cli/internal/room: pass; no issues
make architecture-check size-check: pass; 181 packages, 1864 files, 27470 functions
make fmt and git diff --check: pass
```

## Bounded runner repair and consumer evidence

`run.py` now drains stdout/stderr concurrently while retaining at most 64 KiB
per stream, records total bytes/truncation, bounds TERM and KILL waits, and
bounds reader cleanup. The deterministic control at
`runs/timeout-control-yunwms9u/result.json` intentionally floods each stream
with 1 MiB and ignores SIGTERM: it retained 4096 bytes per stream, sent
SIGTERM then SIGKILL, reaped after SIGKILL, stopped both readers and completed
in `0.517546s`.

The fresh standalone consumer was built and run from source
`a7302e076768d730cce0cfac7997be6b4fc84969`:

```text
build: pass; executable SHA256 1fbf0b5ae564a67856ef7d5d505331c455e2a9999385d3acb5f0f049bc9f8cfc
positive: pass; 21/21 literal and overflow/alignment cases; exit 0; clean shutdown
negative-control: pass; child exit 1; actual_samples=480 mutated_expected=481; clean shutdown
timeout-control: pass; child exit -9 by bounded SIGKILL; both 1 MiB streams capped; reaped
```

Fresh records are `artifacts/build.json`,
`runs/positive-k021hpb7/result.json`,
`runs/negative-control-kaaiabw_/result.json`, and
`runs/timeout-control-yunwms9u/result.json`. Their SHA256 values are,
respectively, `c76ecd8a59fd5af5430efa0ceb17ab2706d54454794f432daf981aad84b9bac6`,
`40930a3f6879f17bf6aae5b4d1b7e613d96eaf2a3636fd90fb769582a49a5181`,
`ca84a487734109acd2770ab3dc36d4226068192633c754eed2cbb1e1621cb4d3`, and
`ed46966ee6290214b7297e55b2e635f8dcac7e9038ece599b88962ee3e494f8f`.
`artifacts/artifact-manifest.json` is fresh at source `a7302e0` with SHA256
`75f4de9eb7019eff1419a09dfa4bf0d475efed8f805029eac0f5c580706f4b91`.
An explicit `--source-root $FACTORY_ROOT` run was rejected before execution
with `build provenance source root mismatch`, confirming a different checkout
cannot silently reuse this binary or relabel its source.

## Same-source shipped yui audio/tool replay

`artifacts/yui-verification.json` is fresh at source
`a7302e076768d730cce0cfac7997be6b4fc84969`; the yui executable SHA256 is
`6215fd5328131393167ed3bc0effa2e6f62d1d0d5272b1edac78c7c79621eec9`. Its
tracked Go/module input digest covers 1922 files with
`91615bcc684c8559cfa07d02a29289c7a43010b6f70eb3ab0eb4f3430a861c6a`; accepted
fixture hashes remain artifact-2
`38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169` and
artifact-3 `154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`.

The fresh v2 tool-trace capture emits `PROBE_TOOL_MARKER_9182` and
`fixture_complete`; directory replay verifies `18` wire events and `1` tool
call. Provider PCM is 4800 bytes with SHA256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`, and
rendered PCM is 3200 bytes with SHA256
`7d2d8221eb8ec0be3da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`.
The interruption capture/replay verifies `15` wire events and `0` tool calls;
provider PCM is 3840 bytes with SHA256
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`, and
rendered PCM is 3360 bytes with SHA256
`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`.
The no-trace strict replay exits `1` with the expected missing
`timeline.jsonl` diagnostic. Every fresh yui child returned within 60 seconds
and reports bounded reader cleanup.

The first merged-head tool attempts are preserved under
`runs/merged-a730-tool-trace` and `runs/merged-a730-tool-no-trace`; they failed
before source-valid replay because their copied fixture config referenced a
missing `evidence/runs/exec-invocations-v4.log`. Fresh v2 runs restored that
fixture-relative file and passed. This historical setup failure does not
replace or weaken the required negative controls.

No script-CI green result, independent review, guarded merge, post-merge
vertical acceptance or project completion is claimed here. The next action is
to commit the fresh evidence checkpoint, push the same branch, update PR #422
with exact base/head/provenance, and return `ACCEPTED` to the script CI gate;
do not poll CI locally.

# C30 final repair verification

Final candidate source revision: `4eb31919a0ad6a138dc5c9f88bc5bd03697adbb5`
Branch: `codex/audio-runtime-c30-frame-dimension-safety`
PR: `#422`
Fetched `origin/main`: `1f82284abee0bd31a6680310444cea2e4c16ef00`

The admitted task was reverified with
`project-control.py verify-work --type task --name audio-runtime-c30-frame-dimension-safety`.
The result was `status=admitted`, project `audio-runtime`, and the branch matches
`prd.json.branchName` in this isolated worktree. Required ancestry remains present
for startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and `origin/main`.

## Repaired review findings

- Restored `go-audio/pkg/audio/pcm16_framer.go` and its regression test exactly
  from `origin/main`; removed the co-located duplicate framer definitions while
  keeping the audio package at its immutable 49-file baseline.
- Added checked PCM16 frame samples/bytes and byte-capacity arithmetic, with
  exact sample alignment, platform-int bounds, and wide intermediate handling.
  Mixer duration statistics now use the checked shared helper, including safe
  handling for a rate of `1<<62` with two channels.
- Added the tiny-frame/large-queue constructor overflow cases and the wide
  rate/channel statistics regression before runtime setup.
- The public consumer runner now validates the explicit checkout root, builds
  against that root through a temporary modfile, embeds the source revision,
  and refuses stale or mismatched build provenance, fixture hashes, or binary
  hashes.

## Final gate evidence

```text
focused PCM16 framer/frame/mixer tests: pass (52 tests across go-audio and room)
go test ./go-audio/pkg/audio ./agent-cli/internal/room: pass (285 tests)
go test -race ./go-audio/pkg/audio ./agent-cli/internal/room: pass (285 tests)
go vet ./go-audio/pkg/audio ./agent-cli/internal/room: pass; no issues
make lint: pass; pinned golangci-lint reported 0 issues in every module
make staticcheck: pass
make architecture-size-check: pass; 181 packages, 1864 files, 27365 functions
make architecture-check: pass
gofmt and git diff --check: pass
```

The stale pre-repair build record was rejected before the final rebuild, and an
explicit different source root was honored (its build failed on the absent new
API rather than silently using this checkout). No running host checkout was
merged or reset.

## Final bounded public consumer artifacts

```text
build: pass; source_revision=4eb31919a0ad6a138dc5c9f88bc5bd03697adbb5
positive: pass; child exit_code=0; 21/21 cases passed; clean_shutdown=true
result: runs/positive-_ld49dx5/result.json
negative-control: pass; runner exit=0; child exit_code=1; clean_shutdown=true
result: runs/negative-control-wcvcrc9t/result.json
causal mismatch: actual_samples=480 mutated_expected=481
```

Final artifact hashes:

```text
build.json: 3412acd8e33597b95b104c5f34a707544015ab65faa27694af839bbe1714ec72
artifact-manifest.json: f15b9be602b74839f952f7f328dba28ff137900a221cac4f241492d55fba211f
positive result: 6757733139b3ecfa23cd9a82a5aa04c091be165b0209357de912f9aa6e17244a
negative result: a85dbb16c2bc81999cfcf81f2cf4616a17476deefc2e49d3eb98cc3b77d6c1f4
consumer executable: b4988bb5f7a8d7b0737a2cc2d833ac1624c72da9fdbdcd48d5ca352e3c80738d
```

The predecessor checkpoint below is retained for auditability; its older
result paths are historical and are superseded by the final-head artifacts
listed above.

# Predecessor C30 verification checkpoint (retained)

Candidate source revision: `df08f066140aa16eef784cb862d124c129477c9a`
Branch: `codex/audio-runtime-c30-frame-dimension-safety`
Fetched `origin/main`: `1f82284abee0bd31a6680310444cea2e4c16ef00`
Startup integration revision: `8bdafc7f947a3a2c9856220abdc539437035bd21`
Baseline revision: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`

Both required ancestry checks passed:

```text
rtk git merge-base --is-ancestor 8bdafc7f947a3a2c9856220abdc539437035bd21 HEAD
rtk git merge-base --is-ancestor 3194edd97aed588f7cdf2f8c58a69ac21da4c9ad HEAD
rtk git merge-base --is-ancestor 1f82284abee0bd31a6680310444cea2e4c16ef00 HEAD
```

## Focused causal checks

The pre-fix public-method reproduction and exact output are preserved in
`baseline-reproduction.log`. The old source returned `24000/48000` for the
large rate-duration vector and `4/8` for the overflowing channel vector.

```text
command: rtk proxy go test ./agent-cli/internal/room -run 'TestPCM16.*(FrameSize|Dimension|QueueCapacity)' -count=1 -timeout 60s
exit code: 0
ok  	github.com/portpowered/go-agent-harness/agent-cli/internal/room	0.182s
```

The focused tests cover exact common-rate answers, invalid and fractional
dimensions, sample-valid/byte-invalid boundaries, canonical/legacy sentinel
identity, queue products before cadence/context/channel/goroutine setup,
large capacity statistics, and the old wrap vectors without allocating their
adversarial sizes.

## Accumulated package regressions

```text
command: rtk proxy go test ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s
exit code: 0
ok  	github.com/portpowered/go-agent-harness/go-audio/pkg/audio	0.887s
ok  	github.com/portpowered/go-agent-harness/agent-cli/internal/room	3.928s

command: rtk proxy go test -race ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s
exit code: 0
ok  	github.com/portpowered/go-agent-harness/go-audio/pkg/audio	9.233s
ok  	github.com/portpowered/go-agent-harness/agent-cli/internal/room	4.854s

command: rtk go vet ./go-audio/pkg/audio ./agent-cli/internal/room
exit code: 0
Go vet: No issues found

command: rtk make architecture-check size-check
exit code: 0
architecture gate passed: 181 package(s), 1864 file(s), 27358 function(s) checked
architecture gate passed: 181 package(s), 1864 file(s), 27358 function(s) checked

command: rtk git diff --check
exit code: 0
```

The existing PCM16 framer implementation and regression were co-located with
the new frame-sizing files so the maintained package-file count stays at the
recorded 49; no architecture baseline or policy file was changed.

## Script-static rejection repair

The canonical task inbox returned PR #422 head
`38f15d822ce265a0e9b4303d71a1a0f4911c39ad` to the executor because the hosted
`CI (static)` job failed its `Run golangci-lint` step. Its gofmt, Wire,
architecture/size, vet, and staticcheck steps succeeded. The pinned local
`rtk make lint` reproduced the two `errcheck` findings in the new room test:
the cleanup callbacks did not check `mixer.Close()` errors.

Repair commit `df08f066140aa16eef784cb862d124c129477c9a` reports close errors
through `t.Errorf` in both callbacks without changing production code or test
deadlines. After the repair, `rtk make fmt`, `rtk make wire-check`,
`rtk make architecture-size-check`, `rtk make vet`, `rtk make lint`, and
`rtk make staticcheck` all exited 0; lint reported `0 issues` for every
module, and the architecture gate remained at 181 packages, 1864 files, and
27358 functions. No CI run was polled locally.

## Bounded public consumer

The evidence-local module builds with `GOWORK=off` and does not modify any
repository module manifest. The exact build and run records are in
`artifacts/build.json`, `artifacts/artifact-manifest.json`, and the two result
files below. All records use candidate source revision
`df08f066140aa16eef784cb862d124c129477c9a`.

```text
command: rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety/run.py --build --source-root .
exit code: 0
status: pass

command: rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety/run.py --positive --source-root .
exit code: 0
status: pass; child exit_code=0; clean_shutdown=true
result: runs/positive-duxk8xod/result.json

command: rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety/run.py --negative-control --source-root .
exit code: 0 (runner success; child intentionally exits 1)
status: pass; child exit_code=1; clean_shutdown=true
result: runs/negative-control-0ycza70a/result.json
causal mismatch: actual_samples=480 mutated_expected=481
```

Executable SHA256: `721500a9a5375125e58aa68d4d969954713dd7f39089ded81ec2ab8c2610b653`

Fixture SHA256 values are recorded in the artifact manifest for `consumer/go.mod`,
`consumer/go.sum`, and `consumer/main.go`. No CI checks were polled locally;
script CI, independent review, guarded merge, and the post-merge exact-source
consumer/physical validation remain external gates.
# C30 current consolidation and exact-head evidence

Implementation checkpoint: `20af5a5f9147da7d40cfe61f068bc70c76ef510b`
(`refactor audio: consolidate PCM16 framer sizing`). The branch is
`codex/audio-runtime-c30-frame-dimension-safety`; fetched `origin/main` is
`1f82284abee0bd31a6680310444cea2e4c16ef00`. Startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21`, baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and `origin/main` are ancestors.

The amended C30 lease was used to fold the unchanged 44-line framer source and
51-line framer test into the owned `pcm16_frame_size.go` and
`pcm16_frame_size_test.go`, preserving all framer symbols, behavior and
assertions while deleting only the redundant files. `format.go` and
`pcm16_convert_test.go` remain byte-identical to `origin/main`; no architecture
baseline or policy file changed. This resolves Review139's owned-path finding
and the exact pre-repair architecture failure (`go-audio/pkg/audio` 51 > 49).

## Causal and accumulated checks

```text
go test ./go-audio/pkg/audio ./agent-cli/internal/room -run '^TestPCM16' -count=1 -timeout 60s: pass; 73 tests
go test -race ./go-audio/pkg/audio ./agent-cli/internal/room -run '^TestPCM16' -count=1 -timeout 60s: pass; 73 tests
go test ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s: pass; 285 tests
go test -race ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s: pass; 285 tests
go vet ./go-audio/pkg/audio ./agent-cli/internal/room: pass
make architecture-size-check: pass; 181 packages, 1864 files, 27365 functions
make architecture-check size-check: pass
make lint: pass; pinned golangci-lint 2.9.0, 0 issues in every module
make staticcheck: pass; pinned staticcheck 2026.1
make wire-check: pass; generated Wire unchanged
make fmt and git diff --check: pass
```

The focused tests include the reduced rate-duration boundary, channel-product
overflow, invalid and fractional dimensions, literal common-rate answers,
sample-valid/byte-invalid limits, tiny-frame/large-queue construction before
cadence setup, and the wide-rate/two-channel `Stats` duration regression. The
legacy methods continue to return `ErrMixerInvalidFormat` through `errors.Is`.

## Exact-head public consumer

The bounded consumer was rebuilt and run from the implementation checkpoint
above. `run.py` validated the explicit checkout root and temporary module
replacement; no existing module manifest changed.

```text
build: pass; source=20af5a5f9147da7d40cfe61f068bc70c76ef510b
positive: pass; child exit=0; clean_shutdown=true; 21/21 cases passed
negative-control: pass; runner exit=0; child intentionally exit=1; clean_shutdown=true
negative mismatch: actual_samples=480 mutated_expected=481
```

Consumer executable SHA256:
`cedca24d3a60e2873cf66178c6cfa281736b791062a1382251954f5126ec6a34`.
Consumer fixture hashes are recorded in `artifacts/build.json` and
`artifacts/artifact-manifest.json`:

```text
consumer/go.mod  5ea71077ade9ef97a00666347af21a9ac1928ae3ecd2531e44657531230ed060
consumer/go.sum  2ab5bab01b0637952a659bcdf79ca2e54754d2fce4fd743305f8ca9e792e419d
consumer/main.go 347c3110dd1ccdc838fca16fbb986059cda21cd2c890bf0c6a41c092e12ddd38
```

The current artifact hashes are `build.json`
`80697e1c9974a406993183967fa408cfb4c11b2dd4431194741a75926aeacf7a`,
`artifact-manifest.json`
`3321b08c9b80f124c23c9ee3d1ee4d9f0761b8835d7f97c298d81d974fbabe97`,
positive result
`68c58bdb416d91ffe779c8f3d3480dc026115915bffa2241f348ad10e68177de`, and
negative result
`35b8a74faf3a0276143d1a48809f4b18ed09fcd1b99a7d6a067cc391b66eba73`.

## Same-source shipped yui audio/tool replay

`artifacts/yui-verification.json` records source
`20af5a5f9147da7d40cfe61f068bc70c76ef510b`, yui SHA256
`6fd688d254aa99ab84c298fc2c8096f3cc451f8b7245f12b33bd61afd1bfe0e9`, and the
aggregate hash
`0b20bb2a19d9fb4ece9f5a45099d9a8dfd798c27bd64a89523fefed34eccbc3d` over 1,921
tracked Go/module build inputs. Accepted fixture hashes remained
`artifact-2.json=38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169`
and
`artifact-3.json=154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`.

The tool case captured and replayed successfully with `18` wire events and `1`
tool call. Provider PCM is `4800` bytes with SHA256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`; rendered
PCM is `3200` bytes with SHA256
`7d2d8221eb8ec0be3da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`. The
interruption case captured and replayed successfully with `15` wire events and
`0` tool calls. Provider PCM is `3840` bytes with SHA256
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`;
rendered PCM is `3360` bytes with SHA256
`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`. The
no-trace strict negative control exited `1` with the expected missing
`timeline.jsonl` diagnostic. All children returned within 60 seconds and were
launched in separate process groups.

These are software/file replay results only. No physical device, acoustic,
Realtime, CI-green, review, merge, vertical acceptance, or project completion
claim is made here.

## Provenance rule for the evidence-only descendant

The executable inputs above are pinned to implementation source
`20af5a5f`. Any later commit that updates this ledger or generated report is an
evidence-only descendant and must not relabel these artifacts as a new source
build. Before handoff, verify that `git diff --name-only
20af5a5f9147da7d40cfe61f068bc70c76ef510b HEAD` contains only the owned evidence
directory; the source revision and executable/input hashes above remain the
truthful tested provenance. A source or executable-input change requires a new
build and fresh runs.

# C30 current-main consolidation and exact-head refresh

The latest admitted-board finding required current-main integration before any
new evidence. `git fetch origin main` resolved
`431fc96c14f0e0045629d9c36f98ee61ff06e840`, which was merged into this isolated
branch as `8e42efb3ddb3820c032cceb9c6707c8773bb48f4`. Required baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21`, planning main
`1f82284abee0bd31a6680310444cea2e4c16ef00`, and fetched current main are all
ancestors. The running host checkout was not merged or reset. The unrelated
untracked `meta-operator-throughput-feedback.md` remains untouched.

The amended C30 lease was applied for the final architecture shape: the
unchanged PCM16 framer symbols and regression assertion remain in the owned
`pcm16_frame_size.go` and `pcm16_frame_size_test.go`, while the redundant old
framer files are removed so the audio package remains at its immutable 49-file
budget. `format.go` and `pcm16_convert_test.go` are unchanged against
`origin/main`. Checked sizing, queue-product preflight, wide Stats duration and
the `ErrMixerInvalidFormat` legacy delegation remain in the implementation.

## Focused and structural evidence

```text
go test ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s: pass; 285 tests
go test -race ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s: pass; 285 tests
go vet ./go-audio/pkg/audio ./agent-cli/internal/room: pass; no issues
make architecture-check size-check: pass; 181 packages, 1866 files, 27510 functions
git diff --check: pass
```

The pre-refresh stale consumer record was rejected before execution with
`build provenance revision mismatch` (`a7302e07` versus the requested repair
head). The refreshed bounded consumer is built from exact source
`727b789f9423c84eb70d899ddf2da1c52837dc4f`, with executable SHA256
`bf69965133ee822554bfc2b689c3f44ece42f66a35d8d5b8837adfbdf1c5b87e`:

```text
run.py --build --source-root <isolated-worktree>: pass
run.py --positive --source-root <isolated-worktree>: pass; 21/21 literal cases; clean shutdown
run.py --negative-control --source-root <isolated-worktree>: pass; child exit 1; actual_samples=480 mutated_expected=481
run.py --timeout-control --source-root <isolated-worktree>: pass; 1 MiB stdout/stderr capped, SIGTERM then SIGKILL, reaped, reader threads stopped
run.py --build --source-root $FACTORY_ROOT: rejected before execution; host source lacks PCM16FrameSamples/PCM16FrameBytes/PCM16ByteCapacity
```

The consumer artifact and manifest are refreshed at the exact source head;
`build.json` and `artifact-manifest.json` carry the same source revision,
explicit source module, fixture hashes and executable hash.

## Same-source shipped yui refresh

`artifacts/yui-verification.json` is regenerated as schema v3 from the same
`727b789f` source. The yui executable SHA256 is
`f05fe81bedc314a1bfed344ef503003b41c88791ab0485821a77fcd8db14dc50`. Its
tracked Go/module input digest covers 1,925 files with SHA256
`6e406672e834a8f5563892cd93075b960f759239f014374af2a03312bdd156df`.
Fixture hashes remain artifact-2
`38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169` and
artifact-3 `154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`.

The current tool fixture emits `PROBE_TOOL_MARKER_9182`, strict continuation
and `fixture_complete`; its bundle has 18 wire events and one tool call,
provider PCM 4,800 bytes with SHA256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`, and
rendered PCM 3,200 bytes with SHA256
`7d2d8221eb8ec0be3da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`. Directory
replay exits 0 with the same 18-event/one-tool verification.

The no-trace control captures successfully without an audio trace, and strict
directory replay exits 1 with the expected missing `timeline.jsonl` diagnostic.
The interruption fixture captures 15 wire events and zero tool calls, with
provider PCM 3,840 bytes / SHA256
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22` and
rendered PCM 3,360 bytes / SHA256
`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`.
The first immediate bounded replay observed the bundle before
`timeline.jsonl` became visible; the same bounded command was rerun after the
bundle stabilized and exited 0 with `Replay verified: 15 wire events, 0 tool
calls`. Both outcomes are retained in the v3 report; no failure was relabeled.
All yui children used a 60-second child deadline and a 90-second outer
`testtimeout` budget, with no Realtime or physical-device use.

No current-head script-CI success, independent review, guarded merge,
post-merge vertical acceptance or project completion is claimed. The next
action is to checkpoint and push this changed same-task candidate, update PR
#422 with the current-main ancestry and exact-head evidence, and return
`ACCEPTED` to the script-owned CI gate without polling it.

# C30 current-main exact-source handoff

The latest canonical rejection required current-main integration before new
provenance. `git fetch origin main` resolved
`c61ee2774986c896560ee40a92441c914976000d`, which was merged into this isolated
branch as `154d2543ad4e3d310bb7e4861027c7d826674a9f`. Startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21`, baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and fetched current main are
ancestors. The running host checkout and unrelated untracked
`meta-operator-throughput-feedback.md` were preserved.

The amended C30 lease was used to keep the PCM16 framer symbols and regression
assertion in the owned frame-sizing source/test files while removing only the
redundant co-located files. `format.go` and `pcm16_convert_test.go` remain
byte-identical to `origin/main`; the audio package remains at 49 Go files.
Checked sizing, queue-product preflight, wide Stats duration, and legacy
`ErrMixerInvalidFormat` delegation are unchanged by the integration.

## Focused and structural evidence

```text
go test ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s: pass; 293 tests
go test -race ./go-audio/pkg/audio ./agent-cli/internal/room -count=1 -timeout 60s: pass; 293 tests
go vet ./go-audio/pkg/audio ./agent-cli/internal/room: pass; no issues
make architecture-size-check: pass; 181 packages, 1866 files, 27532 functions
git diff --check: pass
```

The wide-rate Stats regression covers `rate=1<<62`, `channels=2`, and
`1,953,125ns` without a zero denominator or duration wrap. Tiny-frame and
large-queue constructor tests prove input/output byte products are rejected
before cadence setup, goroutines, channels, or allocations. Direct exported
consumer checks pass 21/21 literal cases, including common-rate answers,
fractional/sign/zero rejection, reduced rate-duration sizing, channel-product
overflow, and byte-capacity boundaries. The mutated expected `480 -> 481`
negative control exits 1 with `actual_samples=480 mutated_expected=481`.

## Exact-source bounded consumer

`run.py --build --source-root .`, `--positive`, `--negative-control`, and the
ignored-SIGTERM output-flood `--timeout-control` all pass from source
`154d2543ad4e3d310bb7e4861027c7d826674a9f`. The consumer SHA256 is
`9dec8109925ff9447c4863413b2fa5f50697ee277a65a7af092e3c056e995ae0`; the
runner records the source root, module, revision, fixture hashes, executable
hash, capped output, process-group cleanup, and clean shutdown. An explicit
different source root is rejected before execution with
`build provenance source root mismatch`.

## Same-source shipped yui regression

`artifacts/yui-verification.json` records a `yui` build from the same tested
source with executable SHA256
`963720da43bbd04e26192edffb2a2048374eb0fba064b4a6da1adcbbb566b7f6`. The
tracked Go/module input set contains 1,929 files with aggregate SHA256
`e7e0b8eea8008bf0e1c0d2451b27f5557fc587863985294559cfc9dbed6e1ca6`.

The corrected tool capture/replay passes 18 wire events and one tool call,
with provider PCM 4,800 bytes / SHA256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502` and
rendered PCM 3,200 bytes / SHA256
`7d2d8221eb8ec0be3da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`. The
interruption capture/replay passes 15 wire events and zero tool calls, with
provider PCM 3,840 bytes / SHA256
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22` and
rendered PCM 3,360 bytes / SHA256
`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`.
The no-trace capture is retained and strict replay exits 1 for the expected
missing `timeline.jsonl`. The first tool attempt's missing-directory failure is
preserved at `runs/current-154d-tool-trace/failure.json`; it was repaired by
precreating `evidence/runs`, not by changing source. All fresh children used
the 60-second child and 90-second outer deadlines, with no Realtime or physical
device use.

No current-head script-CI success, independent review, guarded merge,
post-merge vertical acceptance, or project completion is claimed. The exact
next action is to commit and push this changed same-task candidate, update PR
#422 with the current-main ancestry and fresh hashes, and return `ACCEPTED` to
the script-owned CI gate without polling it; any exact CI rejection remains
with this executor for `CONTINUE` repair.
