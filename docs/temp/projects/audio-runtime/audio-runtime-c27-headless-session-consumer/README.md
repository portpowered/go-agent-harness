# C27 headless session consumer

This directory is a small, independent Go module that consumes the public
`go-agent-runtime/services/session` and `services/session/wire` contracts. It is
deliberately outside `go.work`: every command is run with `GOWORK=off`, uses a
deterministic injected `messages.Inferencer`, and receives absolute storage and
workspace paths in one JSON document on stdin.

The executable accepts no flags or positional arguments. Its scenarios exercise
construction, streaming, durable continuation across processes, independent
concurrent services, cancellation, idempotent close, and expected-failure
controls. `scripts/verify.py` is the hermetic runner. It builds the module and
launches the executable with an allowlisted environment and no terminal.

The `package` action also extracts the published source archive into a fresh
directory without `.git`, copies the declared descriptor inputs, and runs the
archived verifier's `verify-artifacts` action there. Dependency graph hashes
are canonicalized to exclude Go's extraction/cache-specific `Dir` and `GoMod`
paths; the six replacement source-tree hashes bind the extracted source bytes.

Examples (the verifier supplies the complete JSON configuration):

```text
GOWORK=off go build -trimpath -o ./bin/headless-session ./cmd/headless-session
printf '%s\n' '{"scenario":"boundary", ...}' | ./bin/headless-session
```

No credentials, network, device, CLI package, ambient configuration, or hidden
global state is required by the consumer.
