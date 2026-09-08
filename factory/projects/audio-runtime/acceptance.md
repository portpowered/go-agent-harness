# Immutable acceptance

Only the operator can revise these criteria. A slice may own a subset, but must
name later gates for the rest. Project completion requires every criterion and
separate customer and engineering validation; a baseline merge is only one slice.

## AUDIO

One independently testable audio subsystem owns packet parsing, formats, clocks, sample timing, DSP and buffer operations; the core loop uses buffers without direct device IO.

## DEVICE

The adjacent device gateway owns physical device abstractions and lifecycle; playback evidence distinguishes actual consumption from queue admission.

## EMBED

A separate Go module constructs and exercises the runtime without CLI imports, flags, terminal state or hidden global initialization.

## SERVICE

Services expose thin services/X contracts with private services/X/internal implementations and per-service Wire construction; CLI transport delegates business behavior.

## TRACE

Correlated device capture/playback, provider send/receive, tool lifecycle, cancellation, queues/drops and terminal evidence use explicit timing domains and bounded recording resources.

## REPLAY

Credential-free replay preserves recorded audio packets, ordering, tool events, interruption and termination; hardware and external nondeterminism limits are explicit.

## FAILURES

Truncated/no playback, barge-in failure, long-conversation slowdown and tool-continuation collisions have reproducible characterization, fixes where demonstrated, and exact residual limitations without false completion claims.

## QUALITY

Architecture boundaries, package/file/function size, complexity, mutable globals, generated Wire consistency and relevant compile/static/race checks pass without growing migration baselines or weakening assertions.

## PARITY

Supported CLI behavior is preserved and final integrated behavior has fresh independent customer and engineering evidence, with authorized bounded Realtime proof and explicit physical-device evidence limits.
