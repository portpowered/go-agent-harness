# Factory handoff baseline status

Recorded September 6, 2026. Code checkpoint: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` on
`codex/embeddable-agent-runtime`. The tree was clean at that checkpoint.
This is a reproducible stabilization checkpoint, not a declaration that main
merge readiness or full migration acceptance has passed.

## Completed bounded slices

- `b3038b75`: streaming gateway capture contracts, ordered settlement, canonical
  envelope serialization and integrity tests; size baseline reductions only.
- `e8c17bec`: bounded recording spool with protected temporary files, admission
  limits, ordered commit/discard, cleanup, and atomic publication.
- `303b18e8`: independently constructible runtime device probe service.
- `3194edd9`: CLI probe delegation, whole tool-service replacement, explicit
  provider recording injection through reproducible Wire graphs, test helpers,
  and independent consumer dependency metadata.

## Verified at the integration checkpoint

- Full architecture and size gate: 180 packages, 1,834 files, 26,362 functions;
  no issues and no baseline growth.
- `make wire-check`: all eight registered graphs regenerate without drift.
- Focused CLI custom tool positive/negative integration and composition tests.
- CLI Wire, tools, device contracts and private device service tests under race.
- Runtime device/provider/recording tests under race; scoped probe, provider,
  recording, tool adapter and application Wire lint checks.
- `GOWORK=off go test -race ./...` in `tests/embedding`: passes as an external
  module after tidying its actual runtime probe dependencies.
- Gateway capture suite and focused capture race tests passed during the bounded
  slice; no later gateway source edits were made.
- `git diff --check`: clean.

## Remaining before autonomous handoff and baseline merge

1. Implement and test the lightweight graph and durable single-project admission
   specified in `handoff-plan.md`. The current graph is still the legacy graph.
2. Prove recovery, cross-project rejection, CI/review routes, manager packet
   preflight, and independent validation using the installed factory runtime.
3. Run a bounded real factory dispatch on an isolated endpoint and record its
   actual runtime, session and resume identity. No new factory is running yet.
4. Fetch current main, pin its SHA, and have a Luna max worker integrate this
   checkpoint in an isolated branch. Re-run applicable checks on that actual
   merge result, then require independent Luna review and required CI.
5. Resolve or precisely characterize remaining release-gate failures. The full
   CLI integration/release suite has not been freshly demonstrated green. A root
   lint attempt reported a self-play contextcheck issue outside the bounded
   slices; an unused catalog helper from that report was subsequently removed.
   Do not treat scoped lint as a full-repository lint pass.
6. Refresh integrated recording/replay and bounded live Realtime proof on the
   final artifact. Earlier live evidence predates these latest changes and
   cannot establish current physical device capture/playback behavior.

The migration project must remain open after baseline integration. Its original
criteria include canonical timing/DSP/buffers, physical device consumption
tracing, remaining CLI business-logic extraction, per-service private ownership,
bounded conversation projections, and the reported audio/continuation failures.
Retain these in the admitted immutable acceptance contract.
