# C118 session-terminal retirement evidence

`inventory.json` records the admitted project, immutable source baseline,
dual-ancestry checks, caller census, peer lease exclusions, and the absence of
prior C118 review or CI rejection findings.

`external-consumer` is a separate Go module. With `GOWORK=off`, its test imports
only the public `go-agent-runtime/services/sessionterminal` contract, its
generated Wire constructor, and the public provider taxonomy. It verifies
typed error identity, deterministic continuation metadata, accounting,
cancellation/output-state policy, and independent service construction.

The evidence is vertical only. Shared Wire registration and architecture-size
baseline edits remain deferred until the C79 lease is released. Script CI,
independent review, guarded merge, and the post-merge probe remain external
handoff gates.
