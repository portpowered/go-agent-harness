# C53 provider-close terminal-drain evidence

This folder is the task-local evidence surface for
`audio-runtime-c53-provider-close-terminal-drain`. `consumer/main.go` is a
separate Go module and uses only exported `messages`, `session`,
`session/wire`, and `audio/clock` contracts. Its deterministic provider queues
the interruption, delayed cancelled audio, healthy response, and
response-scoped `SESSION.CLOSE`, then closes the provider immediately after
admitting that queue. The public event collector is the oracle: it requires
healthy `TEXT.DELTA`, provider-authored `MESSAGE.END`, concrete close metadata,
and one final terminal in that order.

`run.py` builds the consumer and current YUI artifact with `-trimpath`, runs
the base control from an immutable `origin/main` archive, controls every child
process group, caps retained output, and records hashes and cleanup. The
shipped replay reads the predecessor's immutable credential-free capture and
configuration without modifying that worktree; its output is written only
under this folder's ignored evidence artifacts. `verify.py` checks causal
ordering, negative controls, input/output hashes, ancestry, C05/C23
preservation, and the bounded resource ledger.

This is software/provider simulation evidence only. It does not claim physical
device consumption, acoustic output, Realtime access, quiet-host performance,
or project-level acceptance. `ACCEPTED` hands the exact pushed head to script
CI; it does not claim CI green.
