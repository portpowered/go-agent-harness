# Multi-turn duplex multiplex proof

Status: **achieved and formally proven in-repo** (2026-08-26).

This proof extends `agent-cli/test/integration/session_duplex_overlap_test.go`,
the v8 CLI-driven duplex replay harness. It runs two generated shipped
`agent session` commands concurrently against hermetic T1 replay captures and
couples their raw PCM boundaries directly:

```text
agent session --replay <temporary-capture> --audio-in - --audio-out - --max-duration 1s "<instruction>"
```

Harness A uses `harness-A: answer with the amber profile`; harness B uses
`harness-B: answer with the cobalt profile`. A's `--audio-out -` is written
directly to B's `--audio-in -`, and B's output is written directly to A's
input. Transcript text never transports audio between harnesses. The proof
does not call a live provider, use a transcript bridge, or bypass the CLI with
direct loop/provider evidence. Replay captures include `SESSION.CLOSE`, so the
replay runtime remains alive for all turns before its normal close boundary.

## Deterministic schedule

Both command graphs receive the same `*clock.Deterministic` instance. The
coordinator uses logical ticks and timestamps, not wall-clock ordering:

| Turn | Direction | Start tick | Boundary |
|---|---|---:|---|
| 1 | A → B and B → A | 7 | overlap |
| 2 | A → B and B → A | 8 | overlap |
| 3 | A → B | 9 | sequential |
| 3 | B → A | 10 | sequential |

The first two turn groups are separate user-starts-before-prior-agent-finish
windows. Turn 3 supplies an ordinary non-overlapping boundary. The bridges
hold each raw output boundary until the peer consumes it and accepts its
matching runtime audio-input observation. They do not send response-cancel or
truncate an earlier response. Each bridge is bounded, observes context and
coordinator abort, and emits EOF only after all three frames have crossed.

The test reuses `go-agent-loop/testdata/audio/overlap_16k.wav`, selecting three
separated non-silent 480-sample PCM16 frames as distinct per-turn identities.
No audio fixture is added or modified. Temporary replay JSON, four recording
JSON files, and four WAV files are created under `t.TempDir()` and read back by
the verifier.

## Per-turn ledger and parity

The positive verifier requires six ordered crossings, each with a harness
direction, stable key (`A-turn-1` through `A-turn-3` or `B-turn-1` through
`B-turn-3`), schedule ordinal, logical tick, deterministic timestamp, emitted
PCM, delivered PCM, SHA-256, and RMS. It also requires three runtime audio
outputs, three runtime audio inputs, three accepted client-to-server input
commits, three completed-turn events, and one clean terminal event per CLI. The
multi-turn raw stdin reader returns an explicit `audio.ErrEndOfTurn` marker
between PCM frames. `FileSource` preserves the persistent stream at that
marker, and the session audio producer sends `MESSAGE.END` before reading the
next frame. The observed session wrapper emits an input-commit observation only
after the underlying session accepts that `MESSAGE.END`; the observation owns
the exact PCM accumulated since the previous commit and its one-based ordinal.
The verifier binds commit *n* to the corresponding directional crossing,
transcript marker, completed turn, deterministic timestamp, exact PCM/hash/RMS,
and successful replay acceptance. The runtime observations are produced inside
the session command; the test coordinator only gates the raw stream and records
what the runtime reports.

The four views are:

```text
A/client   B/agent
B/client   A/agent
```

Every view is checked independently against the directional crossing ledger,
then its sender/client and peer/agent counterpart are compared for exact turn
identity, direction, order, tick, timestamp, payload bytes/hash, and RMS. JSON
and concatenated PCM16 WAV artifacts are read back against the same per-turn
live ledger. Aggregate counts or concatenated equality alone are not
sufficient. The expected final logical tick is 10, and both CLIs must return
within the one-second command bound, close their input/output streams and
replay sessions, and settle to the established goroutine tolerance.

## Deterministic negative controls

The positive run and positive verifier are reused unchanged by four controls:

* `TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnAudioControl` replaces
  turn 2 in the delivered `B/agent` view with turn 1's non-silent PCM. The
  crossings, command results, turn counts, and overlap counts remain plausible;
  the per-view ledger rejects the exact turn-specific hash/RMS mismatch.
* `TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnTranscriptControl` assigns
  A's turn-2 marker to B's turn 2. The command and runtime counts remain
  unchanged; the ordered transcript ledger rejects the wrong harness/turn
  attribution and reports the expected and observed markers.
* `TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls/missing_commit`
  removes A's second accepted input-commit observation. The remaining ordinal
  is now attributed to turn 2, so the unchanged verifier reports the expected
  ordinal 2 and observed ordinal 3 for stable `B-turn-2`.
* `TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls/cross-attributed_commit`
  replaces A's second committed PCM with turn 1's PCM. The commit count and
  command outcome remain plausible, but exact hash/RMS and crossing parity
  reject the cross-attribution.

Both mutations happen after the real CLI run, do not modify committed
fixtures, and retain the same clock, bridge, lifecycle, and verification path.
Diagnostics name the affected harness or direction, stable turn identity or
ordinal, field, and expected-versus-observed identity. The commit controls
mutate only runtime evidence after the underlying CLI has already accepted all
three commits, so they exercise the verifier's later-turn attribution checks
without bypassing the production path.

## Focused reruns and scope

Run the proof from `agent-cli` with:

```bash
go test ./test/integration -run 'TestSessionCLI_DuplexPCM(MultiTurn|Overlap)' -count=1 -v
go test -race ./test/integration -run 'TestSessionCLI_DuplexPCM(MultiTurn|Overlap)' -count=1
```

This is an extension of the existing one-overlap v8 proof, not a second
orchestration harness and not barge-in cancellation. The existing one-overlap
test remains in the same file and keeps its silence control. Evidence is
behavioral: command execution, emitted/delivered PCM, runtime observations,
turn-specific transcript markers, recording parity, artifacts, bounds, and
cleanup. No source inventory, registration scan, documentation-link check, or
asset-bundle meta-test is used as proof.
