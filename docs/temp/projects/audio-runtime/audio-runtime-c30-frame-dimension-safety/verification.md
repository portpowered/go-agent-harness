# C30 verification checkpoint

Candidate source revision: `1e3a15e97179185750bc1531ff6d8b75ca7fa57b`
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

## Bounded public consumer

The evidence-local module builds with `GOWORK=off` and does not modify any
repository module manifest. The exact build and run records are in
`artifacts/build.json`, `artifacts/artifact-manifest.json`, and the two result
files below. All records use candidate source revision
`1e3a15e97179185750bc1531ff6d8b75ca7fa57b`.

```text
command: rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety/run.py --build --source-root .
exit code: 0
status: pass

command: rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety/run.py --positive --source-root .
exit code: 0
status: pass; child exit_code=0; clean_shutdown=true
result: runs/positive-o55l3f6m/result.json

command: rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c30-frame-dimension-safety/run.py --negative-control --source-root .
exit code: 0 (runner success; child intentionally exits 1)
status: pass; child exit_code=1; clean_shutdown=true
result: runs/negative-control-bwoilxph/result.json
causal mismatch: actual_samples=480 mutated_expected=481
```

Executable SHA256: `721500a9a5375125e58aa68d4d969954713dd7f39089ded81ec2ab8c2610b653`

Fixture SHA256 values are recorded in the artifact manifest for `consumer/go.mod`,
`consumer/go.sum`, and `consumer/main.go`. No CI checks were polled locally;
script CI, independent review, guarded merge, and the post-merge exact-source
consumer/physical validation remain external gates.
