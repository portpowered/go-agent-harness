# C82-02 public service and independent consumer checkpoint

The reusable boundary is now under `go-agent-runtime/services/sessionlive/`.
The root exposes only host-neutral contracts and value/types; mutable lifecycle
state, loop construction, timing, binding, action serialization, drain order,
and cleanup joining live in `internal/service`. `services/sessionlive/wire`
owns the dedicated generated composition graph.

Evidence:

- `go-agent-runtime/services/sessionlive/contract.go` contains no CLI, provider,
  device, credential, environment, `init`, or excluded-runtime dependency.
- `rtk rg -n 'agent-cli|internal/services/internal/agentruntime|func init\(|os\.(Getenv|LookupEnv)|session_(runtime_plan|options|tools|diagnostics)' go-agent-runtime/services/sessionlive`
  returned no matches.
- `rtk go test ./go-agent-runtime/services/sessionlive/... -count=1 -timeout=120s`
  passed 12 tests across 3 packages.
- `rtk go test -race ./go-agent-runtime/services/sessionlive/... -run
  'TestRun|TestNewLoop|External|Service|Wire|Setup|Bind|Instruction|Tool|Transcription|Response|Order|Done|Drain|Cancel|Deadline|ProviderError|Close|Join|Survivor' -count=3`
  passed 30 tests across 3 packages.
- The independent `GOWORK=off` consumer imports only `sessionlive` and
  `sessionlive/wire`; normal and race runs both passed.
- `rtk proxy env GOWORK=off go generate ./wire`, run from
  `go-agent-runtime/services/sessionlive`, regenerated the pinned Wire graph;
  the normal Wire construction test and `-tags=wireinject` guard passed.

The generated-file architecture registration is recorded in the architecture
policy. The shared `scripts/wire-packages.txt` registry remains untouched while
its exact C57 lease is active.
