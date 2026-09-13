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

The latest same-task continuation at `8b2983338ea449fd739994e8fbbc958581bd2e7b`
repeated the owned checks: sessiontrace normal `24` tests across 3 packages,
sessiontrace race `20` tests across 3 packages, CLI trace normal `25`, CLI trace
race `24`, the GOWORK=off consumer, and accumulated normal regressions at
`COUNT=1` all passed. Fetched `origin/main` is
`ea53be13ce5e4ef14fd8c89c695c21744a1f7686` and is not yet an ancestor. The
shared gates still fail only on the C79-leased findings: Wire registration for
`go-agent-runtime/services/sessiontrace/wire/wire_gen.go` and the stale
`prepareTrace` architecture baseline/generated-file entries. C79
`work-task-25` remains `in-review` with PR #470 open, so those files remain
untouched. After C79’s guarded merge and explicit lease release, integrate
current main and apply only those demonstrated downward/shared repairs.
