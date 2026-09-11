# C48 overlapping tool continuation

This is a credential-free, source-file-built public-runtime reproduction for
`audio-runtime-c48-overlapping-tool-continuation`. It imports only exported
`messages`, `session`, `session/wire`, and `audio/clock` contracts. It does not
import Realtime providers, CLI packages, or runtime-internal packages.

The fixture publishes two provider response IDs and all four distinguishable
tool calls before the first tool result. The tool executor completes beta before
alpha within each batch; the runtime must still forward results in correlated
call order exactly once. The fixture then keeps response 1 open while response
0 produces its grounded continuation, and only closes response 1 after that
continuation. This exercises the public live adapter across continuation,
coordinator/model assembly, and tool-result forwarding boundaries without
using sleeps or serializing the two provider responses.

The bounded driver builds the consumer against the current workspace, requires
at least 2 GiB of free storage, runs the positive probe, and runs deliberate
`missing`, `duplicate`, and `swapped` result controls. It writes all generated
reports below this directory's ignored `artifacts/` path and reaps timed-out
process groups.

Run from the repository root:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c48-overlapping-tool-continuation/run.py
```

The positive report is accepted only when it proves both response IDs preceded
the first result, observes four exact provider calls and four exact forwarded
results, verifies the public trace and terminal, and records reverse executor
completion. Control runs must fail with an explicit fixture error.
