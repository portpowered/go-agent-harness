# s2s v8 milestone — CLI-driven duplex PCM overlap is ACHIEVED

Status: **achieved and formally proven in-repo** (2026-08-26).

The v8 milestone proves that two shipped `agent session` command instances can
be coupled through raw PCM in both directions on one deterministic logical
timeline. The proof is hermetic, credential-free, network-free, and CI-gating.
Its executable evidence is
[`TestSessionCLI_DuplexPCMOverlap`](../../agent-cli/test/integration/session_duplex_overlap_test.go)
and its explicit negative control is
`TestSessionCLI_DuplexPCMOverlapRejectsSilenceControl` in the same file.

## Exact CLI drive

The test constructs two generated shipped CLIs and starts both commands at the
same barrier. The command shape is:

```text
agent session --replay <temporary-t1-capture> --audio-in - --audio-out - --max-duration 1s "<instruction>"
```

The two instruction profiles are deliberately different and are recorded with
their command results:

```text
harness-A: answer with the amber profile
harness-B: answer with the cobalt profile
```

The test owns only the stdin/stdout boundary. A's raw `--audio-out -` bytes are
written directly to B's `--audio-in -`; B's raw output is written directly to
A's input. No transcript text, transcript-derived bytes, session-loop call,
provider call, or replay-helper call is used as the inter-harness connection.

## T1 fixture and deterministic timeline

Each invocation receives a test-local `gwtesting.SessionCapture` replay file
with synthetic T1 provenance (`synthetic-t1/session-replay`). Its ordered wire
shape is:

1. server `SESSION.OPEN`;
2. client `TEXT.DELTA` carrying that harness's instruction;
3. server `AUDIO.DELTA` carrying the scripted response frame;
4. client `AUDIO.DELTA` carrying the peer PCM expected by replay;
5. client type-only `MESSAGE.END` after audio-input EOF;
6. server `AUDIO.END` and `MESSAGE.END`.

The captures and all output artifacts live under `t.TempDir()` and are never
added to the committed fixture corpus. The test reuses the committed
`go-agent-loop/testdata/audio/overlap_16k.wav`, selecting two separated
non-silent 480-sample PCM16 frames (16 kHz mono). It also reuses
`silence_16k.wav` for the negative control.

Both CLIs receive the same `*clock.Deterministic` instance through the
composition port swap. Its timeline is:

| Property | Value |
|---|---|
| Base timestamp | `2026-08-26T12:00:00Z` |
| Tick duration | `10ms` |
| First/overlap crossing | logical tick `7` |
| Final asserted tick | logical tick `8` |
| Command bound | `1s` per `agent session` |
| Orchestrator bound | `2s`, plus a bounded cleanup window |
| Observed turns / allowed bound | `1` / `2` |

The coordinator retains exactly two crossings in order: A-to-B and B-to-A.
Both are stamped at tick 7, so their derived timestamp is exactly
`2026-08-26T12:00:00.070Z`; this is the declared overlapping speech window.
The verifier requires both emitted and delivered PCM to equal the expected
non-silent frame and to exceed the corpus VAD threshold (RMS > 300).

## Four-view parity and lifecycle evidence

The run records four independently inspectable views, each as both JSON and
PCM16 WAV under the temporary run directory:

```text
A-client.json   A-client.wav
A-agent.json    A-agent.wav
B-client.json   B-client.wav
B-agent.json    B-agent.wav
```

The JSON records payload bytes, SHA-256, RMS, direction, order, logical tick,
and deterministic timestamp. Sender/client and peer/agent views are compared
cross-harness (`A/client` ↔ `B/agent`, `B/client` ↔ `A/agent`) for exact
payload identity and all metadata. The JSON and WAV artifacts are then read
back and checked against the live recordings. Both terminal fact sets must
match and report clean CLI return, one observed turn, input EOF, output frame,
and final tick 8. The test also waits for the accepted goroutine baseline
tolerance after each run.

## Negative control

`TestSessionCLI_DuplexPCMOverlapRejectsSilenceControl` runs the same two CLI
commands, instructions, clock, B-to-A path, bounds, and verifier while replacing
the first A-to-B delivered frame with the first PCM frame of committed
`silence_16k.wav`. The temporary B replay capture expects that substituted
wire payload only so the actual CLI run reaches the shared audio verifier; the
verifier's expected A-to-B payload remains the original non-silent overlap
frame. No committed fixture is changed.

The test passes only when the verifier rejects the run with a diagnostic that
includes the `A-to-B` direction, `logical tick 7`, expected versus observed
`hash=...`, and expected versus observed `RMS` (the observed silence RMS is 0).
Thus plausible transcript/event counts cannot make a silent PCM crossing pass.

## Scope boundary

This is distinct from:

- v1 text-in/audio-out, which does not couple two live command instances;
- v2a basic audio input, which proves one session's ingress rather than a
  bidirectional crossing;
- v6a authentication errors, which prove an error contract rather than audio;
- the lower-level functional duplex fixture, which does not execute two
  generated shipped CLI command surfaces.

No production code or broad audio-corpus edits are needed for v8; the existing
overlap and silence assets are sufficient.
