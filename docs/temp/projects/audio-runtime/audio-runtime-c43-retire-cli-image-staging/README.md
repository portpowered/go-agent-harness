# C43 retire CLI image staging

This admitted slice moves session image staging out of the CLI adapter and
behind the public `go-agent-runtime/services/tools` contract. The private
`services/tools/internal/imagestaging` package owns filesystem permissions,
extension selection, path advertisement, refresh decoration, and idempotent
cleanup. The CLI only resolves its host-owned configuration directory and
delegates to `services/tools/wire.NewImageStaging`.

The `consumer/` module is an evidence-only public consumer. It imports the
public tools contract and Wire constructors, resolves the actual public
`read_image` tool, stages a literal PNG, executes the tool through the public
executor, checks the typed image result, checks refreshed tool definitions,
and verifies cleanup plus a post-cleanup negative control. Its `negative` mode
uses a deliberately wrong PNG oracle and must fail; it is not a waived
acceptance path.

Run the bounded evidence verifier from the repository root:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c43-retire-cli-image-staging/verify.py --mode focused
```

The verifier uses no credentials, does not contact a live provider, and does
not poll CI. Generated run logs remain below the ignored `runs/` directory.
