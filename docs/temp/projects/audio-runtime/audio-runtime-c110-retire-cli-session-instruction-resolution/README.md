# C110 session instruction resolution evidence

This is the admitted evidence folder for
`audio-runtime-c110-retire-cli-session-instruction-resolution`.

The `consumer` directory is a separate Go module. It imports only the public
`services/sessioninstructions` contract and its dedicated Wire constructor,
then exercises literal, file, `AGENTS.md`, explicit `none`, scope, skill-order,
composition, invalid-loader, and cancellation behavior. It does not import the
CLI, private runtime packages, credentials, or ambient filesystem state.

The canonical implementation is stateless and receives all host I/O through an
injected loader. Resolution rejects malformed or oversized text, preserves
causes with phase-specific `ResolutionError` values, checks cancellation around
loader calls, and clones capability snapshots before policy composition.

The committed verification command is:

```text
./verify.py --mode positive-and-guard-mutations
```

It runs the consumer with `GOWORK=off`, checks direct imports and the thin CLI
adapter, and records bounded JSON reports under `reports/` when reports are
requested. These artifacts are implementation evidence only; they do not claim
CI, review, merge, or project-wide acceptance.
