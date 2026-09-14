# C69 verification record

All commands below were run in the isolated C69 worktree with the repository's
`rtk` command wrapper. No Realtime session, physical device, or acoustic claim
is made by this slice. PR #458 is open; CI was not polled.

## Current executor checkpoint

The admitted task and branch were reverified before this checkpoint. The
fetched `origin/main` remains
`d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`; startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21` and planning main are ancestors of
the tested source. The tested source tree was clean at
`a6c2bc0bd604dd3e154f9b928a913b614d1aa04b`; the follow-up ledger commit is
documentation-only and does not change executable inputs.

Fresh focused evidence on that source passes:

```text
go-agent-runtime/services/bareadmission normal: 16 tests across 3 packages
go-agent-runtime/services/bareadmission race COUNT=3: 48 tests across 3 packages
CLI bare-session compatibility normal/race: 27 tests each
GOWORK=off external consumer normal; race COUNT=3: pass
COUNT=1 scripts/test-session-ci-regressions.sh all: normal, coverage, race pass
coverage gate over 7 generated profiles: 179 registered packages pass
make fmt, make vet, pinned make lint, pinned make staticcheck: pass
git diff --check: pass
```

The accumulated matrix retains its expected mismatch, PCM, transcript and
provider-control negative diagnostics; no assertion, deadline, baseline or
output cap was weakened. The standalone consumer constructs the public Wire
service and proves explicit resolution, typed redacted failure, cancellation,
immutability and deterministic repetition.

The remaining gate output is unchanged and lease-scoped:

```text
make wire-check: one unregistered generated path,
  go-agent-runtime/services/bareadmission/wire/wire_gen.go
make architecture-check: that same one generated-file registration finding
make architecture-size-check: five stale C69 session_bare.go entries plus
  that same generated-file registration finding
```

The generated Wire registry remains owned by C57 and the architecture baseline
remains owned by C61. No shared file was edited, no CI result was polled or
claimed green, and no C69 review row or prior review finding exists in the
canonical `~default` inbox. After those leases release, integrate the exact
accepted main, make only the C69 registration and five downward/deletion
changes, rerun the gates, and submit this same task to script CI without
polling.

## Passing focused evidence

```text
rtk go test ./go-agent-runtime/services/bareadmission/... -count=1
Go test: 16 passed in 3 packages

rtk go test -race ./go-agent-runtime/services/bareadmission/... -count=3
Go test: 48 passed in 3 packages

rtk go test ./agent-cli/internal/services/internal/agentruntime -run 'Test(ResolveBareSession|ResolveRealtimeSessionProvider|NewLiveSessionInferencerCarriesBareAudioPolicies|BareSession|BrowserToolsMinimal)' -count=1 -timeout=300s
Go test: 27 passed in 1 packages

rtk go test -race ./agent-cli/internal/services/internal/agentruntime -run 'Test(ResolveBareSession|ResolveRealtimeSessionProvider|NewLiveSessionInferencerCarriesBareAudioPolicies|BareSession|BrowserToolsMinimal)' -count=1 -timeout=300s
Go test: 27 passed in 1 packages

rtk proxy env GOWORK=off go test ./... -count=1    # external consumer module
ok example.com/audio-runtime-c69-bareadmission-consumer

rtk proxy env GOWORK=off go test -race ./... -count=3    # external consumer module
ok example.com/audio-runtime-c69-bareadmission-consumer

rtk make coverage-registration
coverage registration passed: 179 workspace packages checked across 6 modules

rtk make coverage-changed COVERAGE_BASE=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06
coverage gate passed, including the 80.00% bareadmission package floor

rtk make fmt
all workspace Go modules passed formatting validation

rtk make vet
all workspace Go modules passed go vet

rtk make lint
all workspace modules passed pinned golangci-lint v2.9.0 with 0 issues

rtk make staticcheck
all workspace modules passed pinned staticcheck 2026.1
```

The focused matrices cover provider/session/default precedence, OpenAI and
Grok policy defaults, explicit empty model, typed provider/model/transport
failures, credential ordering and redaction, BaseURL precedence, VAD and
transcription policy, both device directions, nil config/catalog behavior,
input/result ownership, deterministic repetition, and cancellation. The
separate `GOWORK=off` module imports only the public runtime module and uses its
Wire constructor.

## Architecture and Wire gate results

After the local complexity, test-boundary, and mutable-global repairs:

```text
rtk make architecture-check
architecture gate failed with 1 issue(s)
- generated-file-spoof services/bareadmission/wire/wire_gen.go: generated header is not registered with a reproducible generator

rtk make architecture-size-check
architecture gate failed with 6 issue(s)
- five baseline-stale entries for the retired session_bare.go symbols
- the same unregistered bareadmission Wire generated file

rtk make wire-check
wire-check: Wire registry mismatch: unregistered=['go-agent-runtime/services/bareadmission/wire/wire_gen.go'], outside modules=[]
```

The remaining findings are shared-file lease dependencies, not local service
violations. `scripts/wire-packages.txt` and
`docs/architecture/architecture-size-baseline.json` were deliberately left
unchanged under the implementation handoff.

## Next gate action

Do not submit unchanged script CI while the two exact shared leases remain
active. Once C57 releases
the Wire registry lease and C61 releases the architecture-baseline lease,
integrate the exact accepted current main, make only the downward C69 registry
and five stale-entry deletions, rerun the focused gates, then submit the same
task/current head to script CI without polling it.
