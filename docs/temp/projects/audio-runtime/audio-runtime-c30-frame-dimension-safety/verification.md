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
