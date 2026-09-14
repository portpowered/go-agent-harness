# C95 source census

The moved implementation was one 472-line CLI-owned ledger. Its runtime
callers remain unchanged:

| Caller | Use retained at the adapter boundary |
| --- | --- |
| `session_room_run.go:529,1082` | Route source PCM through the target participant ingress ledger |
| `session_room_run.go:964` | Resolve a mixed frame with `provider_input_rejected` |
| `session_room_run.go:1127` | Resolve a mixed frame with `participant_output_rejected` |
| `session_room_replay_scheduler.go:265` | Route replayed source PCM through the same ingress path |
| `session_room_orchestration.go:191` | Preserve mixer-ready observer forwarding |
| `session_room_orchestration.go:351` | Construct one ledger per participant |
| `session_room_lifecycle.go` | Retain the participant runtime ledger field |

The public contract owns the stable event, field, disposition, reason, source,
and bounded-observation vocabulary. The private service owns the mutable map,
per-source FIFO, concurrent resolution, content classification input,
downstream rejection projection, 32-observation cap, cumulative counters,
pending terminal loss, sorted source projection, and idempotent summary.

The host adapter retains only the narrow mixer translation required because the
public runtime module cannot import `agent-cli/internal/room`: mixer write
dispositions and host sentinel errors become the public disposition/reason
vocabulary. It contains no ledger state, FIFO, totals, sorting, or summary
policy.

The public package has no imports of CLI, mixer, device, flag, terminal,
credential, environment, or global-runtime code. The only composition import
is the generated `roomaudiodiagnostics/wire` graph.
