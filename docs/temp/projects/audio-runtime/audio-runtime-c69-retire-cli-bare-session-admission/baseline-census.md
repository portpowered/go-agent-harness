# C69 bare-session admission census

This record is scoped to project `audio-runtime`, contract revision
`audio-runtime-v1`, and task `audio-runtime-c69-retire-cli-bare-session-admission`.
It is evidence for the isolated candidate, not whole-project acceptance.

## Immutable provenance

| Item | Value |
| --- | --- |
| startup integration revision | `8bdafc7f947a3a2c9856220abdc539437035bd21` |
| planning origin/main | `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06` |
| fetched execution origin/main | `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06` |
| candidate branch | `codex/audio-runtime-c69-retire-cli-bare-session-admission` |
| implementation checkpoint | `38e2ddba71b67a53d07204d9bc43a2e86260a82d` |
| final pushed PR head | `d85daa6a0780bd64ba08e49ed77374394f1ee2d0` |
| pull request | `#458` |
| accepted-main production file SHA-256 | `2aadc03e24f3b3dcad97d7a04d25f61e9541275a790674168d3f79487948fb74` |
| candidate production file SHA-256 | `5a44be4cae20845844e7c64403749c4837912e32e8502f5aa7c78b51b3f98221` |
| candidate public contract SHA-256 | `79bf38607a6be13979130e488a06bd68e0cee50b1c79dd4a443afcbd1f3db087` |
| candidate private service SHA-256 | `9ee35e16ba77f254efc98d77471a6ca494a1a7684b41467549a85003f8300a92` |
| candidate contract-test SHA-256 | `2b60a17a9247af6dd8ff9ad9dac95d21c789c68a3189bb9e64313f476bafdd44` |

The admission command was run from the factory root:

```text
rtk proxy python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c69-retire-cli-bare-session-admission --root "$FACTORY_ROOT"
{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c69-retire-cli-bare-session-admission"}
```

The startup and planning commits are ancestors of the candidate. The accepted
main production file count is `271`; the candidate count is `222`, for a
production-file net retirement of `49` lines. The count excludes tests,
evidence, generated Wire output, new runtime lines, and peer paths.

## Accepted-main declaration inventory

The accepted-main `agent-cli/internal/services/internal/agentruntime/session_bare.go`
declared the following policy surface:

```text
ErrBareSessionCredentialMissing
ErrUnsupportedBareSessionProvider
BareSessionCredentialError
(*BareSessionCredentialError).Error
(*BareSessionCredentialError).Unwrap
ResolveBareSessionOptions
bareSessionAPIKey
loadBareSessionConfig
bareProviderConfig
resolveBareSessionTransport
resolveBareSessionTurnDetection
resolveBareSessionTranscription
```

The only production call site outside the adapter remains
`agent-cli/internal/services/internal/agentruntime/service.go:216`; CLI tests
exercise the adapter directly.

## Candidate retained adapter inventory

The candidate retains only the CLI-edge declarations below in
`session_bare.go`:

```text
ErrBareSessionCredentialMissing              alias to public service sentinel
ErrUnsupportedBareSessionProvider            alias to public service sentinel
BareSessionCredentialError                   alias to public typed error
ResolveBareSessionOptions                    Deprecated CLI-edge adapter
mapBareSessionAdmissionError                historical CLI error mapping
mapBareSessionTurnDetection                 result mapping
mapBareSessionTranscription                 result mapping
snapshotBareSessionCatalog                  model-catalog adaptation
snapshotBareSessionConfig                   loaded-config adaptation
cloneBareBool                               snapshot helper
loadBareSessionConfig                       CLI-owned config/file boundary
```

`ResolveBareSessionOptions` loads already-owned CLI configuration, snapshots
the injected environment value, maps the existing model catalog and selectors
to `bareadmission.Request`, performs one Wire service call, and maps the owned
result back. Provider/model/transport/default/credential/VAD/transcription/
device policy is implemented in the private runtime service.

## Shared-file dependencies preserved

The candidate intentionally does not edit either shared file:

* `scripts/wire-packages.txt`: C57 retains the active exclusive Wire registry
  lease. The new generated path is currently the sole unregistered Wire graph.
* `docs/architecture/architecture-size-baseline.json`: C61 retains the active
  baseline lease. The five stale entries are the exact retired C69 CLI symbols
  listed above; no baseline value was raised.

After those exact leases release, the next C69 action is to integrate the exact
accepted current main, add only the generated Wire registration and delete only
the five demonstrated stale C69 entries, then rerun the architecture and Wire
gates before current-head script CI submission.
