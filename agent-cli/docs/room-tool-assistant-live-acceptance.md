# Room tool-assistant live acceptance

This is a short, manual, billed OpenAI Realtime run for the committed
`room-tool-assistant.json` fixture. It is not a CI test. The fixture gives the
customer no tools and gives the assistant only `exec`; the assistant must
create and verify the proof file before speaking its confirmation.

## Safety and prerequisites

Run from the repository root with Go 1.24.2 or newer, `jq`, `cmp`, and
the OpenAI Realtime credential file available at
`~/.you-agent-factory/secrets/OPENAPI_API_KEY`. The manifest contains only the
environment-variable name `OPENAI_API_KEY`; never put the credential in the
manifest, a command argument, a log, or an evidence artifact.

The proof path is one exact path. The cleanup below removes only that path;
the run directory is created outside the checkout and is never copied into
the repository.

## Procedure

```bash
set -euo pipefail

REPO_ROOT="$(pwd)"
PROOF_PATH="/tmp/room-proof-s2s-room-tool-wielding-participants.txt"
RUN_ROOT="$(mktemp -d)"
RUN_OUT="$RUN_ROOT/evidence"
mkdir "$RUN_OUT"

if [ -e "$PROOF_PATH" ] || [ -L "$PROOF_PATH" ]; then
  rm -f -- "$PROOF_PATH"
fi
test ! -e "$PROOF_PATH" && test ! -L "$PROOF_PATH"

make -C agent-cli build
AGENT_BIN="$REPO_ROOT/agent-cli/bin/agent"

# Install the trap before reading the key so every exit path unsets it.
trap 'unset KEY OPENAI_API_KEY' EXIT HUP INT TERM
KEY=$(tr -d '\r\n' < ~/.you-agent-factory/secrets/OPENAPI_API_KEY)
export OPENAI_API_KEY="$KEY"
unset KEY

"$AGENT_BIN" room run \
  --manifest "$REPO_ROOT/agent-cli/docs/room-tool-assistant.json" \
  --out "$RUN_OUT" \
  > "$RUN_ROOT/room.stdout" \
  2> "$RUN_ROOT/room.stderr"
```

The command should end with `reason=max_turns_reached`. If you stop it after
the required observation, `reason=stopped` is also acceptable. A `failed`
reason is not evidence of success. Keep `room.stdout` and `room.stderr`
private; they are not PR artifacts.

## Sanitized checks

Inspect only the credential-free fields below. This deliberately omits system
and opening prompts from the PR summary while retaining the participant
contract, bounds, connection status, and artifact names.

```bash
jq '{schema_version, timing, bounds, termination_reason,
    participants: (.participants | with_entries(.value |= {
      id, provider, model, api_key_env, voice, tools,
      completed_turns, termination_reason, connected, artifacts
    }))}' "$RUN_OUT/run-manifest.json"

jq -e '
  (.termination_reason == "max_turns_reached" or .termination_reason == "stopped") and
  (.participants | length == 2) and
  (.participants.customer.connected == true) and
  (.participants.assistant.connected == true) and
  (.participants.customer.tools == []) and
  (.participants.assistant.tools == ["exec"])
' "$RUN_OUT/run-manifest.json"
```

The assistant's sanitized delta stream must contain one completed `exec` tool
call. In the JSONL evidence, inspect the `TOOLCALL.END` value for its
`tool_call_id` and `name`, then inspect the single tool-result message emitted
after execution for the same ID. The provider-facing OpenAI wire observation
must show one `function_call` and exactly one accepted/delivered
`function_call_output` carrying that same call ID. The customer delta stream
must contain no tool-call or tool-result records.

Do not count a tool-call start or argument delta as another call, and do not
publish raw audio, authorization headers, environment dumps, or unsanitized
provider errors. Record only the call ID, `exec` name, and the fact that the
result was accepted/delivered with the same ID.

Finally, verify the real side effect. Both checks are required: `test -f`
combined with `test ! -L` proves the path is a regular file rather than a
symlink, while `cmp` proves there is no trailing newline or other extra
content.

```bash
test -f "$PROOF_PATH" && test ! -L "$PROOF_PATH"
printf ROOMPROOF | cmp -s - "$PROOF_PATH"
printf 'proof file content: '
od -An -v -c "$PROOF_PATH"
```

The expected content is exactly `ROOMPROOF` (9 bytes). Save the sanitized
`run-manifest.json` projection, the call-ID correlation, and the proof-file
content in the PR description. Keep all files under `$RUN_ROOT`, outside the
checkout, and let the trap unset `OPENAI_API_KEY` when the shell exits.

## Offline validation

The fixture is covered by a hermetic manifest-loader test. Run these from the
repository root before or after the billed run:

```bash
make -C agent-cli test
make typecheck
make vet
```
