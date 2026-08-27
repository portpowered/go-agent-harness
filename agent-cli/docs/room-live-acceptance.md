# Room Phase 1 live acceptance

This is the manual acceptance procedure for the N-participant room. It is an
on-demand, billed provider run and is deliberately not part of required CI.
The deterministic mesh, mixer, lifecycle, evidence, SSE, and visualizer tests
remain the automated evidence for this feature.

## Prerequisites and safety

Use a workstation with:

- Go 1.24.2 or newer and the checked-out workspace.
- A real OpenAI Realtime account with access to
  `gpt-realtime-2.1-mini`. The room CLI currently supports OpenAI and Grok
  Realtime session providers; an OpenRouter or ordinary chat-completions key
  is not a substitute.
- `curl`, `jq`, and a browser. The browser only consumes event metadata; the
  room SSE endpoint never carries or plays audio.
- Two unused loopback ports, one for the room stream and one for serving the
  static visualizer.

This run incurs provider usage. Perform it manually, outside CI, with a short
bound, and stop it as soon as the required observations are complete. Never
put a key in `room.json`, a committed file, a command-line argument, or a
captured log. The manifest stores only the environment-variable name
`OPENAI_API_KEY`.

## Procedure

The supported command shape is:

```text
agent room run --manifest <file> [--out <dir>] [--stream <addr>]
```

Run these commands from the repository root. The temporary directory and all
artifacts stay outside the checkout:

```bash
make -C agent-cli build
AGENT_BIN="$(pwd)/agent-cli/bin/agent"
RUN_ROOT="$(mktemp -d)"
RUN_OUT="$RUN_ROOT/evidence"
mkdir "$RUN_OUT"

cat > "$RUN_ROOT/room.json" <<'JSON'
{
  "schema_version": 1,
  "room": {
    "max_turns": 3,
    "max_duration": "90s"
  },
  "participants": [
    {
      "id": "alpha",
      "system_prompt": "You are Alpha in a three-person conversation. Speak briefly, address the other participants by name, and complete at least three turns. Begin by greeting them when the room starts.",
      "provider": "openai",
      "model": "gpt-realtime-2.1-mini",
      "api_key_env": "OPENAI_API_KEY",
      "tools": []
    },
    {
      "id": "beta",
      "system_prompt": "You are Beta in a three-person conversation. Speak briefly, respond to Alpha or Gamma, and complete at least three turns. Do not use tools.",
      "provider": "openai",
      "model": "gpt-realtime-2.1-mini",
      "api_key_env": "OPENAI_API_KEY",
      "tools": []
    },
    {
      "id": "gamma",
      "system_prompt": "You are Gamma in a three-person conversation. Speak briefly, respond to the other participants, and complete at least three turns. Do not use tools.",
      "provider": "openai",
      "model": "gpt-realtime-2.1-mini",
      "api_key_env": "OPENAI_API_KEY",
      "tools": []
    }
  ]
}
JSON

# Enter the key without putting its value in shell history or output.
read -r -s OPENAI_API_KEY
export OPENAI_API_KEY
printf '\ncredential loaded for this shell; the value was not printed\n'
```

In terminal 1, start the bounded room. Keep this terminal visible so the
participant-scoped progress and final room summary can be recorded:

```bash
"$AGENT_BIN" room run \
  --manifest "$RUN_ROOT/room.json" \
  --out "$RUN_OUT" \
  --stream 127.0.0.1:8422
```

The command validates the complete manifest and empty output directory before
opening the provider sessions. It prints the stream URL and only bounded
participant progress. A normal bounded run ends with
`reason=max_turns_reached`; `max_duration_reached` is the safety fallback.
Use Ctrl-C (SIGINT) if the run reaches the required observations before the
bound; that should be recorded as `stopped`.

In terminal 2, serve the static reference page:

```bash
cd agent-cli/docs
python3 -m http.server 8088 --bind 127.0.0.1
```

Open this URL in a browser while terminal 1 is still running:

```text
http://127.0.0.1:8088/room-visualizer.html?events=http%3A%2F%2F127.0.0.1%3A8422%2Fevents
```

If the browser enforces cross-origin restrictions between the two loopback
ports, use a local reverse proxy that serves `agent-cli/docs` and forwards
`/events` to `http://127.0.0.1:8422/events`; keep the visualizer's Events URL
as the proxy's same-origin `/events`. Do not disable browser security. The
page must show `Connected`, a room lifecycle row, at least one row for each of
`alpha`, `beta`, and `gamma` in **Live transcript**, and execution records in
the separate **Diagnostics** panel. The page's footer is an explicit check
that no audio playback was attempted.

To exercise server-side participant filtering after the combined view is
visible, reconnect with the filter set to `beta` (or open the following URL):

```text
http://127.0.0.1:8088/room-visualizer.html?events=http%3A%2F%2F127.0.0.1%3A8422%2Fevents&participant=beta
```

Confirm that only Beta's participant events appear and that the page explains
that room-owned lifecycle events are excluded from a participant-filtered
stream. Press **Disconnect** and record the visible disconnected state. This
is a metadata-only observation; no audio playback is expected or desired.

## Artifact and observation recording

After the room exits, inspect only sanitized, structured fields:

```bash
jq '{schema_version, timing, bounds, termination_reason,
    participants: ( .participants | with_entries(.value |= {
      id, provider, model, api_key_env, voice, tools,
      completed_turns, termination_reason, connected, artifacts
    }))}' "$RUN_OUT/run-manifest.json"

for participant_id in alpha beta gamma; do
  jq -e --arg id "$participant_id" \
    '.participants[$id] | .artifacts and (.completed_turns >= 0)' \
    "$RUN_OUT/run-manifest.json"
  wc -c \
    "$RUN_OUT/agent-$participant_id.wav" \
    "$RUN_OUT/agent-$participant_id.diagnostics.jsonl" \
    "$RUN_OUT/agent-$participant_id.deltas.jsonl"
done
```

Record the following in the acceptance ledger below, or in a private run
record that is linked from the PR without credentials:

- UTC start/end time and the exact sanitized artifact directory.
- The room `termination_reason`; it must be exactly `stopped`,
  `max_turns_reached`, `max_duration_reached`, or `failed`.
- For each participant: `connected`, completed turns, and its independent
  `ended`, `disconnected`, or `error` reason.
- The three WAV, diagnostics, and delta paths, their non-zero sizes, and the
  agreement between diagnostic/delta turn counts and `run-manifest.json`.
- Whether all three sessions reached `session.created`/`SESSION.OPEN`, the
  observed N-1 mixer behavior, any dropped connection, and whether surviving
  participants continued.
- The observed double-talk/VAD behavior, including any wedge threshold
  result, and the visualizer's combined, filtered, connected, and disconnected
  states.

Do not paste raw deltas, authorization headers, environment dumps, or audio
into the PR. The checked-in acceptance document should identify only the
relative sanitized artifact paths and the observations needed to reproduce
the result.

## Mechanical C1 wedge rule

The live report must not call a participant "wedged" based on a transcript
impression. Let `S` be the effective turn-silence expectation configured for
the provider session. A participant is mechanically **wedged** when, while at
least one room participant remains active, neither a
`session_turn_completed` nor a `session_failure` diagnostic record has been
emitted for that participant within `3 * S` of the participant's last
turn-start/input activity. Record `S`, the resulting deadline, the UTC
timestamps, and the diagnostic records used for the decision.

The current room manifest does not expose a turn-silence field. If the live
provider does not report an effective `S`, mark C1 **inconclusive** or
**blocked** rather than inventing a value. A timeout caused by the explicit
90-second room bound is a room-bound observation, not evidence that C1
passed.

## Acceptance ledger

The scenario status vocabulary is deliberately conservative:

- **Observed** means the live run produced the stated evidence.
- **Failed** means the stated negative condition occurred.
- **Blocked** means a prerequisite prevented a live attempt; it is not a
  provider behavior result.
- **Inconclusive** means the run produced insufficient evidence for a
  deterministic claim.

| Scenario | Live mechanism and evidence to record | Status / observation |
| --- | --- | --- |
| A1 | Two participants converse through a bound; record artifacts, turn agreement, and a room bound reason. | **Blocked** in the current run; credentialed two-participant evidence is still required. |
| A2 | This procedure's three simultaneous participants exercise each N-1 mixer; record all three connections, multiple turns, and the room bound. | **Blocked** in the current run; credentialed three-participant evidence is still required. |
| C1 | Apply the `3 * S` rule above from diagnostics and wall-clock timestamps. | **Blocked** in the current run; no effective `S` was observed. |
| C2 | Have two peers overlap into the third participant's mixed input; record VAD boundaries and the absence/presence of a wedge or artifact-induced extra turn. | **Blocked** in the current run; no overlapping-speech behavior was observed. |
| C3 | Compare a speaking peer with a silent third peer against the two-participant control; compare diagnostics and audio measurements. | **Blocked** in the current run; no live silence-equivalence artifacts exist. |
| E3 | Record each participant's session-open/created outcome and any provider connection-count rejection. | **Blocked** in the current run; no simultaneous provider session was attempted. |
| G3 | Record the explicit signal/bound, room reason, independent participant reasons, and continued survivors; do not treat participant-count collapse as a clean room stop. | **Blocked** in the current run; no live explicit termination was observed. |

### Current run record: 2026-08-26

Status: **Blocked before live dialing.** This execution environment did not
have a compatible OpenAI Realtime credential exported. Its local CLI config
selected OpenRouter, which is not a supported room Realtime session provider;
no provider endpoint was contacted and no billed request was made. Therefore
A1, A2, C1, C2, C3, E3, and G3 are all **Blocked**, not passes or provider
observations. No live artifact directory exists for this record.

The next credentialed operator must replace the pending rows with the UTC run
time, sanitized artifact path, actual observations, and the exact room and
participant reasons. A deterministic test run must not be relabeled as this
live evidence.

## Automated evidence

The live run is not required CI. Run deterministic validation separately from
the repository root:

```bash
make -C agent-cli test
make typecheck
make vet
```

These commands prove the offline contract only; they do not establish that a
provider accepted three simultaneous billed Realtime connections.
