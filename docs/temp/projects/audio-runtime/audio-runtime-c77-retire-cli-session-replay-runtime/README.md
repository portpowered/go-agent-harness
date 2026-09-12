# C77 replay-runtime handoff evidence

This directory is reserved for the C77 task's focused evidence. The external
consumer is a separate Go module and imports only the public
`services/replay` and `services/replay/wire` packages. Its tests use legacy
event-array fixtures written in-process, so they exercise public admission
without importing CLI, internal packages, credentials, or a workspace.

The consumer verifies text and multi-turn audio plans, exact declared rates,
stable malformed-rate identity, deterministic recorded-duration calculation,
provider-close discovery, and cancellation before replay draining.
