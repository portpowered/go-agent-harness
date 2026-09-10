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
