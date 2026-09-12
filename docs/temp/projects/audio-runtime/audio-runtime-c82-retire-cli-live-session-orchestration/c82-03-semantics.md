# C82-03 lifecycle semantics checkpoint

The private service owns one terminal boundary. It quiesces upstream input,
waits for stragglers, optionally drains playback, cancels the loop, stops owned
resources, joins producer errors, drains accepted deltas, and only then runs
the post-flush callbacks. Parent cancellation and deadline identity remain
discoverable through `errors.Is`; provider and cleanup failures are joined.

Focused causal evidence:

- `TestRunDrainsFinalOutputAndJoinsCleanup` preserves final output and the
  cleanup error identity.
- `TestRunPreservesCancellationAndDeadlineIdentity` preserves parent
  cancellation and the typed max-duration sentinel.
- `TestRunJoinsProviderAndCleanupErrors` preserves both provider and cleanup
  error identities.
- `TestFlushPublishedDrainsAcceptedDelta` directly proves the post-`Done`
  output drain; `TestRunStopsDeadlineTimer` proves deadline timer cleanup.
- `verify.py --mode mutation-post-done-drain --expect-failure` first passed
  discovery and the positive controls, then the temporary drain mutation failed
  specifically because the final delta was missing.
- `verify.py --mode mutation-deadline-cleanup --expect-failure` first passed
  discovery and the positive controls, then the temporary cleanup mutation
  failed specifically because the deadline timer was not stopped.
- Service normal and race runs passed after the final decomposition.

No provider, device, playback, diagnostic, or record/replay policy moved into
the reusable service.
