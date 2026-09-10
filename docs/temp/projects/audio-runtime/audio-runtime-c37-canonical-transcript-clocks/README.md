# C37 canonical transcript clocks

This evidence package keeps the transcript timing boundary narrow. The two
existing production adapters now use the public `go-audio/pkg/clock.Real`
source for host-time defaults. Injected `AgentClock` and `ClientMetadata`
remain authoritative; the agent keeps its local sequence when an injected
clock has no `Tick` method, while the client default remains tick zero and UTC.

`cmd/transcript-clocks/main.go` is an external public-API consumer. It imports
only `go-audio/pkg/clock` and `go-agent-loop/pkg/transcript` from the workspace;
it does not add a module manifest or import CLI/internal packages. It exposes
bounded actions for deterministic timing, byte-boundary/parity controls,
expected-value mutations, and a TERM-resistant descendant cleanup control.

The verifier is run from the repository root with explicit artifact paths:

```sh
rtk proxy go build -o "$C37_CONSUMER" \
  docs/temp/projects/audio-runtime/audio-runtime-c37-canonical-transcript-clocks/cmd/transcript-clocks/main.go
rtk proxy go build -tags=nomicrophone -o "$C37_YUI" ./agent-cli/cmd/yui
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c37-canonical-transcript-clocks/verify.py \
  --action all \
  --source "$C37_SOURCE" \
  --evidence docs/temp/projects/audio-runtime/audio-runtime-c37-canonical-transcript-clocks \
  --consumer-binary "$C37_CONSUMER" \
  --yui-binary "$C37_YUI" \
  --no-build --child-timeout 60 --aggregate-timeout 600
```

The runner records bounded child argv/cwd/environment, exit status, raw output
paths, process-group cleanup, source/build-input hashes, and fixture hashes in
the owned evidence directory. The positive deterministic oracle is frozen at
`2026-01-02T03:04:05Z` with a 20ms tick duration. Separate timestamp and tick
mutation children must fail with their corresponding mismatch. Public replay
uses the read-only C21 fixture files and literal audio/tool and interruption
PCM/healthy-tail oracles; it is software/file replay, not physical or acoustic
consumption proof.

`runs/` and local binaries are ignored runtime outputs. The checked-in JSON
reports are refreshed only after an exact-source build; an evidence-only
descendant must preserve the recorded tested source and build-input equality.
Script CI, independent review, guarded merge, and the post-merge vertical probe
remain external delivery gates.
