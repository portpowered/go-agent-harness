# C69 verification record

All commands below were run in the isolated C69 worktree with the repository's
`rtk` command wrapper. No Realtime session, physical device, or acoustic claim
is made by this slice.

## Passing focused evidence

```text
rtk go test ./go-agent-runtime/services/bareadmission/... -count=1
Go test: 13 passed in 3 packages

rtk go test -race ./go-agent-runtime/services/bareadmission/... -count=3
Go test: 39 passed in 3 packages

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

Push the clean checkpoint and update the task PR now. Do not submit unchanged
script CI while the two exact shared leases remain active. Once C57 releases
the Wire registry lease and C61 releases the architecture-baseline lease,
integrate the exact accepted current main, make only the downward C69 registry
and five stale-entry deletions, rerun the focused gates, then submit the same
task/current head to script CI without polling it.
