# C112 session-trace evidence

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
run from the implementation worktree. Script CI, independent review, guarded
merge, and the post-merge vertical probe remain external handoff gates.
