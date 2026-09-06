# Embeddable agent runtime

`go-agent-runtime` assembles application services over `go-agent-loop`. The loop remains the execution engine; applications embed the runtime services to obtain session lifecycle, provider admission, tools, device attachment, persistence and replay. The CLI is one host of these services.

This extraction is in progress. See the [refactor plan and current acceptance checkpoint](../docs/architecture/embeddable-agent-runtime-refactor-plan.md) for remaining legacy owners and known parity gaps.

## Service boundaries

Each service exposes contracts from `services/<name>`, keeps implementation under its own `internal`, and has generated constructors under its own `wire`. An application composition root calls those constructors and injects the resulting interfaces. Ordinary consumers do not import implementation packages or construct a parallel graph.

| Service | Responsibility |
| --- | --- |
| `session` | One-shot, streaming, iterative and continuous invocation lifecycle |
| `providers` | Configured provider/transport admission over the LLM gateway |
| `devices` | Device and finite-file workers attached to canonical media ports |
| `tools` | Request-scoped capabilities, execution and authorization policy |
| `recording` | Session capture finalization and durability completion |
| `replay` | Explicit capture admission and bounded user-action planning |
| `rooms` | Participant orchestration and room evidence |

`go-audio` owns PCM, framing, DSP, analysis, mixing and the clock contracts. `go-device-gateway` owns device backends. Capture/playback workers exchange bounded buffers with the session; the agent loop does not read or write physical devices.

## Host integration

Inject a `messages.Inferencer` for an existing model integration, or inject the public provider service. The host supplies resolved request values and optional tool/store/observer ports. It owns environment/config discovery, paths, credentials, terminal signals and presentation. Constructors are inert; `Run`, `Open` or live `Start` admit invocation resources.

The [independent consumer](../tests/embedding) contains compilable examples and acceptance tests using only public contracts and Wire. Its text tests inject an inferencer, invoke `session.Service.Run`, and inspect both `Result.Text` and typed `Result.Messages`. Its live tests attach `go-audio` media endpoints, verify exact tails/control ordering, cancel invocations and close their handles. It runs with `GOWORK=off`, so successful compilation does not depend on workspace-only package visibility.

For persistence, create `sessionwire.NewFileStoreFactory()`, open an explicit `session.FileStoreOptions` destination and inject the returned `ManagedStore`. A host can instead supply its own `SessionStore` and `TraceStore`. No home-directory layout is implicit in the runtime.

When recording live provider traffic, inject `recordingwire.NewService(source)` through `providerswire.Dependencies.Recording`. Capture finalization runs after provider termination; callers join it through the returned optional `recording.SessionCapture` role. A durability error must be reflected in terminal evidence rather than reported as a complete recording. For directory evidence, call `recording.Service.OpenLiveEvidence` with an explicit destination, submit typed observations and join `Finalize`. The optional `recording.ProviderCapture` role exposes the destination for raw provider capture; supply it as the session request’s `Replay.OutputCapturePath`, then finalize the provider before the directory recorder. An explicit external provider capture path can instead be supplied in `LiveEvidenceOptions`. Finalization streams and hashes the completed raw capture into `provider.json`; missing or invalid evidence is reported as incomplete.

Session-port observations retain PCM offsets, negotiated format, frame lineage, tool events, sequence IDs and timestamps. They remain distinct from raw provider traffic and do not claim physical-device callback timing. `replay.Service.InspectCapture` admits a complete directory archive or raw capture and returns typed protocol, provider, model and replay-plan metadata. Hosts do not inspect capture JSON themselves. `ResolveCapturePath` remains available for callers that need only the admitted protocol capture path. Directory admission checks the manifest, completion status and provider-artifact digest. Raw protocol codecs and HTTP capture still live in gateway/provider infrastructure; retained raw capture history is not yet bounded for arbitrarily long sessions.

## Verification

From the repository root:

```sh
make embed-check
make wire-check
make verify-architecture
```

From this module:

```sh
GOWORK=off go test -race ./...
```

The shape gate covers ownership, contract roots, public type leaks, generated injectors, package/file/function size, cognitive/cyclomatic complexity and mutable globals. New runtime packages receive no historical debt exemptions. Behavioral replay, race, exact-PCM, cleanup and long-session checks remain necessary alongside those static rules.
