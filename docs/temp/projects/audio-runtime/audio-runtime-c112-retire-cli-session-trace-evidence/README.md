# C112 session-trace evidence

The tested source checkpoint is
`ecdf54bc138b58b0454240b519cd1038acd2aec6`, based on accepted
`origin/main d4766c3dbbf2c198142047ead4449d58dd47d485`. The immutable source
baseline is the 119-line `trace.go` with SHA-256
`db9fab41dd02578db5e7b024af869eb762b21ec14c48c64442d4ad9ca3149710`; the
candidate adapter is 49 lines with SHA-256
`6abfe4c37125209159d18a28fa9544fbfcb1c7a630a3cf0b193adf6e34aab177`.

See `candidate-evidence.json` for caller inventory, focused/race/consumer and
shipped-YUI results, exact shared-gate findings, and the acceptance boundary.

This admitted evidence directory contains `consumer`, a separate Go module
that imports only the public `go-agent-runtime/services/sessiontrace` contract
and its generated Wire constructor. It has no `agent-cli` or private-runtime
imports.

The consumer test constructs two independent services with deterministic
clocks, records all four PCM edges plus a runtime event, publishes both
bundles, verifies that same-length PCM mutation changes the immutable WAV
identity, rejects a missing clock, and retains the staged trace without
overwriting an existing destination.

Run the public consumer with:

```text
GOWORK=off go test ./... -count=1
```

The repository’s accumulated session regression set, focused normal/race
tests, pinned lint/staticcheck, vet, formatting, and coverage registration are
green from the implementation worktree. The shipped credential-free replay
probe is green and explicitly makes no physical-device or acoustic claim.
Script CI, independent review, guarded merge, and the post-merge vertical
probe remain external handoff gates. The C79-owned Wire registry and
architecture-size baseline were not edited; their exact local findings are
recorded in the evidence ledger.
