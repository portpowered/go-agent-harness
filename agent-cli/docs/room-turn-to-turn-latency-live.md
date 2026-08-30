# Room turn-to-turn latency live acceptance

This is an opt-in, credential-gated confirmation for the room latency ledger.
The committed [two-participant manifest](room-turn-latency-live.json) is the
canonical scenario: both participants use `gpt-realtime-2.1-mini`, speak terse
turns, and stop at two completed turns within a 30-second room bound.

The room's `--out` directory is the recording. Keep it outside the checkout;
never commit audio, provider captures, credentials, or unsanitized logs.

## Run and derive the timing report

Build the final binary, load the credential interactively, and keep the key in
the child process environment only:

```bash
make -C agent-cli build
umask 077
RUN_DIR="$(mktemp -d "${TMPDIR:-/tmp}/s2s-room-turn-latency.XXXXXX")"
read -r -s OPENAI_API_KEY
printf '\n'
export OPENAI_API_KEY
trap 'unset OPENAI_API_KEY' EXIT HUP INT TERM

./agent-cli/bin/agent room run \
  --manifest agent-cli/docs/room-turn-latency-live.json \
  --out "$RUN_DIR/evidence" \
  >"$RUN_DIR/room.stdout" 2>"$RUN_DIR/room.stderr"

go run ./agent-cli/cmd/room-latency-report \
  -out "$RUN_DIR/evidence" >"$RUN_DIR/room-latency-report.json"
```

`room-latency-report` reads the finalized room bundle and calls the same
`services.ReadRoomLatencyReport` analyzer used by hermetic tests. Its output
contains every eligible transition and the median/p95/max values for the
detection, dispatch, provider, local-output, harness-owned, and direct-total
buckets.

Before sharing evidence, inspect only safe metadata and sizes:

```bash
jq '{schema_version, finalized, elapsed, termination_reason,
     participants: [.participants[] | {id, provider, model, voice,
     completed_turns, connected, ended}], artifacts}' \
  "$RUN_DIR/evidence/run-manifest.json"
wc -c "$RUN_DIR/evidence"/*
```

## Audio checks

Analyze the participant WAVs with the standard-library `wave` module using
20 ms frames. Report the median dBFS among frames at or below −55 dBFS,
the exact-zero frame fraction, and whether the capture introduces a stable
tonal peak relative to the retained baseline. The before/after run must use
the same manifest and analysis thresholds. Do not call the live result a
comparison if no retained pre-change bundle is available; record that gap in
the PR instead.

The final PR evidence should include the sanitized before/after transition
table, the exact analysis commands and results, the 400 ms hermetic target,
the live median/p95 verdict, and any responsible latency bucket when the live
target is missed.
