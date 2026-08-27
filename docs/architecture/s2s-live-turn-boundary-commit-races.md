# s2s-live-turn-boundary-commit-races — OpenAI live confirmation

This is an opt-in, credential-gated confirmation for the two scheduled-audio
boundary races fixed in the CLI. It sends real audio to OpenAI Realtime and
may incur usage charges. The required CI and the deterministic CLI regressions
do not need credentials or a network connection; this procedure is an
additional provider confirmation.

Run the procedure from the repository root. It uses a private temporary
directory for the config, captures, logs, and recording bundles. Do not use a
customer recording, commit any generated file, or paste a raw capture,
`audio/*.pcm`, API key, authorization header, or unredacted CLI log into a PR.

## Prerequisites and secret handling

- OpenAI Realtime access for `gpt-realtime`.
- `ffmpeg`, `ffprobe`, and Python 3 with the standard-library `wave` module.
- A short local recording whose selected segment contains real speech.

Build the CLI and read the key without putting its value in shell history or
in the process argument list:

```bash
make -C agent-cli build
AGENT_BIN="$PWD/agent-cli/bin/agent"
test -x "$AGENT_BIN"

umask 077
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/s2s-live-turn.XXXXXX")"
read -r -s AGENT_MODEL__OPENAI__API_KEY
printf '\n'
export AGENT_MODEL__OPENAI__API_KEY
```

`AGENT_MODEL__OPENAI__API_KEY` is the CLI's supported environment override.
The commands below pass provider and model selection as non-secret flags and
use `--config-dir "$WORK_DIR/config"`, so the run neither changes the normal
`~/.agent-cli` configuration nor needs `--api-key <secret>`.

## Prepare and validate the audio inputs

Set `SPEECH_SOURCE` to a local file containing real speech. The conversion
produces a short 24 kHz, mono, signed 16-bit PCM WAV. Keep the source local;
the file is sent to the provider by the commands below.

```bash
SPEECH_SOURCE="/absolute/path/to/real-speech.wav"
SPEECH="$WORK_DIR/speech-24k-mono.wav"
SILENCE="$WORK_DIR/silence-24k-mono.wav"
STARTUP="$WORK_DIR/startup-ack-24k-mono.wav"

ffmpeg -hide_banner -loglevel error -y \
  -i "$SPEECH_SOURCE" -map 0:a:0 -vn -t 3.0 \
  -ac 1 -ar 24000 -c:a pcm_s16le "$SPEECH"

# Derive silence from the exact decoded frame count. This keeps the silence
# equal-duration and makes every PCM byte exactly zero.
python3 - "$SPEECH" "$SILENCE" <<'PY'
import sys
import wave

speech_path, silence_path = sys.argv[1:]
with wave.open(speech_path, "rb") as speech:
    params = speech.getparams()
    frames = speech.readframes(params.nframes)

if (params.nchannels, params.sampwidth, params.framerate, params.comptype) != (1, 2, 24000, "NONE"):
    raise SystemExit(f"unexpected speech WAV parameters: {params}")
if params.nframes == 0 or not any(frames):
    raise SystemExit("speech input is empty or contains no non-zero PCM sample")

with wave.open(silence_path, "wb") as silence:
    silence.setparams(params)
    silence.writeframes(b"\x00" * len(frames))

with wave.open(silence_path, "rb") as silence:
    silence_params = silence.getparams()
    silence_frames = silence.readframes(silence_params.nframes)
if silence_params != params or len(silence_frames) != len(frames):
    raise SystemExit("silence duration or WAV parameters differ from speech")
if any(silence_frames):
    raise SystemExit("silence WAV contains a non-zero PCM byte")
PY

for input in "$SPEECH" "$SILENCE"; do
  ffprobe -v error -select_streams a:0 \
    -show_entries stream=codec_name,sample_fmt,sample_rate,channels,bits_per_sample,duration \
    -of default=noprint_wrappers=1 "$input"
done
```

The startup-ack probe uses a non-empty turn whose decoded, normalized 16 kHz
PCM payload is between 23,040 and 30,720 bytes. The runtime's 24 kHz-to-16 kHz
resampler uses the same ceiling rule calculated below. The 0.80-second clip is
normally 25,600 decoded bytes; the check makes the bound explicit and fails
before any network call if the selected source is too short or has an unusual
frame count.

```bash
ffmpeg -hide_banner -loglevel error -y \
  -i "$SPEECH" -map 0:a:0 -vn -t 0.80 \
  -ac 1 -ar 24000 -c:a pcm_s16le "$STARTUP"

STARTUP_BYTES="$(python3 - "$STARTUP" <<'PY'
import sys
import wave

with wave.open(sys.argv[1], "rb") as audio:
    params = audio.getparams()
    frames = audio.readframes(params.nframes)
if (params.nchannels, params.sampwidth, params.framerate, params.comptype) != (1, 2, 24000, "NONE"):
    raise SystemExit(f"unexpected startup WAV parameters: {params}")
if params.nframes == 0 or not any(frames):
    raise SystemExit("startup input is empty or silent")
print(((params.nframes * 16000 + 24000 - 1) // 24000) * 2)
PY
)"
printf 'startup decoded PCM bytes: %s\n' "$STARTUP_BYTES"
test "$STARTUP_BYTES" -ge 23040
test "$STARTUP_BYTES" -le 30720
```

## Verify a run without exposing payloads

The helper below reads only event names, counts, safe status fields, and file
sizes. It does not print event payloads or audio. It proves that the matching
`session.updated` arrived before the first append, that the acknowledged
scheduled session update carries an explicit `turn_detection: null` (under
`audio.input` in the current GA shape), that each input has exactly one
append/commit/response request and terminal `response.done`, and that each
recording entry completed.

```bash
verify_run() {
  capture="$1"
  record_dir="$2"
  expected_turns="$3"

  python3 - "$capture" "$record_dir" "$expected_turns" <<'PY'
import json
import pathlib
import sys

capture_path = pathlib.Path(sys.argv[1])
record_dir = pathlib.Path(sys.argv[2])
expected_turns = int(sys.argv[3])
capture = json.loads(capture_path.read_text())
records = capture.get("records", [])

def positions(direction, event_type):
    return [
        index for index, record in enumerate(records)
        if record.get("direction") == direction and record.get("type") == event_type
    ]

def contains_string(value, needle):
    if isinstance(value, str):
        return needle in value
    if isinstance(value, dict):
        return any(contains_string(item, needle) for item in value.values())
    if isinstance(value, list):
        return any(contains_string(item, needle) for item in value)
    return False

def has_explicit_null_turn_detection(value):
    if isinstance(value, list):
        return any(has_explicit_null_turn_detection(item) for item in value)
    if not isinstance(value, dict):
        return False

    # Captures store the provider event envelope around the session payload.
    session = value.get("session")
    if isinstance(session, dict) and has_explicit_null_turn_detection(session):
        return True

    # Current GA Realtime sessions put the field at audio.input.turn_detection.
    audio = value.get("audio")
    if isinstance(audio, dict):
        audio_input = audio.get("input")
        if (isinstance(audio_input, dict) and
                "turn_detection" in audio_input and
                audio_input["turn_detection"] is None):
            return True

    # Retain the same explicit-null assertion for the legacy-compatible flat
    # session.update form, where the field is session.turn_detection.
    if "turn_detection" in value and value["turn_detection"] is None:
        return True
    return False

updated = positions("server_to_client", "session.updated")
appends = positions("client_to_server", "input_audio_buffer.append")
if not updated or not appends or updated[0] >= appends[0]:
    raise SystemExit("session.updated did not precede the first scheduled append")

updates = [
    record for record in records
    if record.get("direction") == "client_to_server" and record.get("type") == "session.update"
]
if not any(has_explicit_null_turn_detection(record.get("payload")) for record in updates):
    raise SystemExit("scheduled session.update did not carry explicit turn_detection:null")

acknowledgements = [
    record for record in records
    if record.get("direction") == "server_to_client" and record.get("type") == "session.updated"
]
if not any(has_explicit_null_turn_detection(record.get("payload")) for record in acknowledgements):
    raise SystemExit("session.updated did not acknowledge effective turn_detection:null")

for event_type in ("input_audio_buffer.append", "input_audio_buffer.commit", "response.create", "response.done"):
    got = positions(
        "client_to_server" if event_type != "response.done" else "server_to_client",
        event_type,
    )
    if len(got) != expected_turns:
        raise SystemExit(f"{event_type} count={len(got)}, want {expected_turns}")

commits = positions("client_to_server", "input_audio_buffer.commit")
requests = positions("client_to_server", "response.create")
dones = positions("server_to_client", "response.done")
for index in range(expected_turns):
    if not (appends[index] < commits[index] < requests[index] < dones[index]):
        raise SystemExit(f"turn {index + 1} does not preserve append < commit < response.create < response.done")
    if index + 1 < expected_turns and not (dones[index] < appends[index + 1]):
        raise SystemExit(f"turn {index + 2} append overlaps turn {index + 1} response")

for code in ("input_audio_buffer_commit_empty", "conversation_already_has_active_response"):
    if any(contains_string(record, code) for record in records):
        raise SystemExit(f"provider collision code present: {code}")

log_path = record_dir / "session-log.jsonl"
entries = [json.loads(line) for line in log_path.read_text().splitlines() if line.strip()]
if len(entries) != expected_turns:
    raise SystemExit(f"session-log entries={len(entries)}, want {expected_turns}")
for index, entry in enumerate(entries, 1):
    input_data = entry.get("input", {})
    response = entry.get("response", {})
    if (entry.get("turn_index") != index or not input_data.get("committed") or
            not input_data.get("audio_bytes") or not response.get("complete") or
            not response.get("audio_bytes")):
        raise SystemExit(f"recording entry {index} is not complete")
    for side in ("in", "out"):
        segment = record_dir / "audio" / f"{side}-{index - 1:03d}.pcm"
        if not segment.is_file() or segment.stat().st_size == 0:
            raise SystemExit(f"missing or empty {segment}")

print(f"verified {capture_path.name}: session.updated precedes first append; {expected_turns} scheduled turn(s) have one client boundary and terminal response; no collision codes")
PY
}
```

## Probe 1: delayed startup acknowledgement

This run uses the bounded startup fixture and one persistent session. There is
no startup sleep; `--max-duration` is only a safety bound. The command exits
with status 0 only after the scheduled response completes.

```bash
STARTUP_RECORD_DIR="$WORK_DIR/startup-recording"
STARTUP_CAPTURE="$WORK_DIR/startup.session.json"
mkdir "$STARTUP_RECORD_DIR"

set +e
"$AGENT_BIN" --config-dir "$WORK_DIR/config" session \
  --provider openai --model gpt-realtime \
  --record "$STARTUP_CAPTURE" --record-dir "$STARTUP_RECORD_DIR" \
  --max-duration 120s \
  --system-prompt "Reply with a short confirmation." \
  --audio-in-turn "$STARTUP" \
  >"$WORK_DIR/startup.stdout" 2>"$WORK_DIR/startup.stderr"
STARTUP_STATUS=$?
set -e
printf 'startup exit: %s\n' "$STARTUP_STATUS"
test "$STARTUP_STATUS" -eq 0
verify_run "$STARTUP_CAPTURE" "$STARTUP_RECORD_DIR" 1
```

The raw capture must show `session.updated` before the first
`input_audio_buffer.append`, followed by exactly one
`input_audio_buffer.commit` and one `response.create`. Do not paste the raw
capture as evidence; retain only the redacted summary described below.

## Probe 2: speech followed immediately by exact silence

This run sends the real speech fixture followed immediately by the equal-size
all-zero fixture in the same persistent session. There is no client-side
delay between the repeatable `--audio-in-turn` values. The acknowledged
`turn_detection: null` configuration leaves no provider VAD boundary to race
the CLI's explicit commit, so the two boundaries remain client-owned.

```bash
TURN_RECORD_DIR="$WORK_DIR/speech-silence-recording"
TURN_CAPTURE="$WORK_DIR/speech-silence.session.json"
mkdir "$TURN_RECORD_DIR"

set +e
"$AGENT_BIN" --config-dir "$WORK_DIR/config" session \
  --provider openai --model gpt-realtime \
  --record "$TURN_CAPTURE" --record-dir "$TURN_RECORD_DIR" \
  --max-duration 120s \
  --system-prompt "Reply with a short confirmation." \
  --audio-in-turn "$SPEECH" \
  --audio-in-turn "$SILENCE" \
  >"$WORK_DIR/speech-silence.stdout" 2>"$WORK_DIR/speech-silence.stderr"
TURN_STATUS=$?
set -e
printf 'speech-then-silence exit: %s\n' "$TURN_STATUS"
test "$TURN_STATUS" -eq 0
verify_run "$TURN_CAPTURE" "$TURN_RECORD_DIR" 2
```

The verifier proves two completed `session-log.jsonl` entries and non-empty
input/output artifacts. It also proves that turn two did not append before
turn one received `response.done`.

## Collision search and redacted evidence

Search only textual artifacts; never search or upload the binary audio
segments. A successful search prints nothing and returns status 1, so the
following command turns any match into a failure:

```bash
if rg -n --glob '*.json' --glob '*.jsonl' --glob '*.txt' \
    'input_audio_buffer_commit_empty|conversation_already_has_active_response' \
    "$WORK_DIR"; then
  printf '%s\n' 'named provider collision found' >&2
  exit 1
fi
printf '%s\n' 'no named provider collision in captures, recording logs, or CLI output'
```

Before sharing results, unset the key and delete or securely retain the
private temporary directory according to local policy:

```bash
unset AGENT_MODEL__OPENAI__API_KEY
```

A safe PR comment contains only a summary like this, with the date and counts
filled in:

```text
OpenAI live scheduled-turn confirmation (gpt-realtime, <date>).
- Startup: session.updated acknowledged turn_detection:null before the first append; one append/commit/response.create/response.done; exit 0.
- Speech + exact silence: two append/commit/response.create/response.done sets; both recording entries completed; turn 2 followed turn 1 response.done; exit 0.
- Collision search: zero input_audio_buffer_commit_empty and zero conversation_already_has_active_response in raw event names/payloads, recording logs, CLI output, and returned status.
- Credentials, raw captures, customer audio, and unredacted payloads were not included.
```

If the live run cannot be performed because credentials or provider access are
unavailable, report that fact explicitly instead of filling in passing live
counts. The hermetic production-CLI regressions remain the mandatory proof:

```bash
go test ./agent-cli/test/integration -run 'TestSessionCommand_LiveScheduledAudio' -count=1
```

Those tests use the real CLI and OpenAI gateway against a credential-free
transport, with observable lifecycle and wire-event synchronization rather
than sleeps.
