# Room PCM16 mixer pressure-path findings

This note records the deterministic pressure trace for the room audio path. It
is intentionally separate from the live acceptance ledger: it contains no
provider credentials, live audio, or billed-run claims.

## Measured shape

The room format is mono, signed little-endian PCM16 at 24,000 Hz with a 20 ms
cadence. One mixer frame is therefore 480 samples, or 960 bytes. A 19,200-byte
`AUDIO.DELTA` contains 20 mixer frames and represents 400 ms of PCM16 audio:

```
19,200 bytes / (24,000 samples/sec * 2 bytes/sample) = 400 ms
```

The regression trace sends three such deltas at 400 ms intervals. Its small
test queues are deliberate so the failure remains time-bounded: 40 input
frames provide 800 ms of storage and four output frames provide 80 ms of
output buffering. The normal case consumes each output frame promptly and
delivers the exact concatenated PCM bytes in order.

## Pressure ownership before the fix

The controlled slow-consumer case reads the first mixed frame and then blocks
at the simulated session-ingestion boundary. The output queue reaches its
four-frame limit first. The mixer cadence goroutine then blocks trying to put
the next frame on that queue, so it stops draining input. The source's first
two complete provider deltas accumulate in the input queue; the next complete
delta observes `ErrMixerInputBufferFull` with the pre-fix synchronous writer.
This distinguishes downstream/session-ingestion blockage from a provider burst
that a healthy 20 ms drain cannot handle.

The production default of 250 input frames is five seconds of audio. The trace
shows why increasing that limit alone is not a fix: a stalled output consumer
eventually consumes any finite input capacity while the mixer is no longer
draining.

## Derived flow-control contract

The supported provider-shaped input is an even, complete 19,200-byte PCM16
delta at the measured 400 ms cadence. The input path must preserve each whole
delta in order; it must not report a successful partial write. When bounded
capacity is temporarily unavailable, the room-facing write must wait for a
frame to drain and remain cancellation-aware. Cancellation, participant
removal, mixer close, and room shutdown must wake the wait and return their
respective terminal error. A slow or permanently stalled session consumer is
therefore bounded by the input and output queues and must propagate pressure
or cancellation rather than being hidden by a larger queue.

The trace establishes the one-delta/400 ms shape and an 80 ms output-queue
window; it does not claim support for an unmeasured larger burst or jitter
window. The delivered mixer retains the five-second input and 160 ms output
safety bounds and owns temporary pressure with a whole-chunk,
cancellation-aware wait at the input boundary. `ErrMixerInputBufferFull` is
reserved for a chunk larger than the entire input queue, which cannot be
accepted atomically without an unbounded allocation or an unreported partial
write. The room fan-out passes the source participant context to that wait;
participant removal, mixer close, internal failure, and room cancellation wake
the writer and preserve the corresponding terminal outcome.
