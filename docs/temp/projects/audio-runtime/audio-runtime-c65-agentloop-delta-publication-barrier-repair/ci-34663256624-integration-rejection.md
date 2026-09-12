# PR 455 integration rejection

The existing candidate was rejected by the script CI integration job at head
`9fb29c504da68bb48b121309ce79221b0b9a9af3`:

- run `34663256624`, job `103469965579`
- URL: `https://github.com/portpowered/go-agent-harness/actions/runs/34663256624/job/103469965579`
- all other required checks were successful; only `CI (integration)` failed
- the deterministic integration and go-agent-loop functional stages passed
- the production-binary audio-device replay failed at
  `TestAgentBinaryTest46HighRateToolAudioRegression/trial_01`
- exact assertion: `remote device rendered 167991/174391 compared samples (lost
  6400, 96.3% retained)`
- playback reported `DroppedSamples:0`, `OverflowEvents:0`,
  `DiscardedSamples:0`, and `DiscardEvents:0`; the failure is the one-block
  continuation/terminal-drain loss, not a public-buffer drop

The complete failed-job log was inspected with `gh run view` before repair.
The board rejection feedback is preserved verbatim here:

`CI checks failed for PR #455 at head 9fb29c504da68bb48b121309ce79221b0b9a9af3: required checks failed on current head 9fb29c504da68bb48b121309ce79221b0b9a9af3: CI (integration)=FAILURE; checks: CI (integration)=FAILURE (https://github.com/portpowered/go-agent-harness/actions/runs/34663256624/job/103469965579). Return this task to the executor with the listed failures.`

The exact 6,400-sample high-rate continuation/terminal-drain boundary is
owned by the admitted C64 task, including the live lifecycle/audio-device
paths. C65 does not edit those paths. Its repair is limited to removing the
unconditional cancellation of the internal loop context after a natural
engine error and before joining the independent delta forwarder; the caller
cancellation branch still cancels that context before joining. This preserves
the C65 publication barrier without changing C64's leased audio-drain
surface.

## Local reproduction after the C65 repair

The shipped replay was run from this worktree with 20 parallel trials and
reproduced the same external boundary:

- trial 06: `167991/174391` compared samples, `6400` lost, with zero dropped,
  overflow, discarded, or discard-event counters
- trial 11: remote playback evidence timed out while the child exited with
  `session audio send failed with status "buffer_full"`
- the other 18 trials passed

No C64-owned source or test path was changed in response to this reproduction.
