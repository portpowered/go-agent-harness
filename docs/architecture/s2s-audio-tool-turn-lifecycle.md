# S2S audio tool-turn lifecycle

Status: implemented in the session runtime; live provider confirmation is an
opt-in operator check.

This document is the contract for a provider-requested tool call in a
bidirectional session. It applies to scheduled audio, streamed audio, text
seeds, and any future input source that enters the same session loop.

## Completion invariant

One provider 'call_id' owns one lifecycle obligation. The obligation is not
complete when local execution finishes, when the result is queued, or when the
provider emits the first 'response.done' for the function-call response. It is
complete only after all of these transitions occur:

~~~
provider request
    -> result accepted at the provider-facing send boundary
    -> exactly one grounded continuation requested
    -> later non-tool assistant MESSAGE.END
~~~

The first 'MESSAGE.END' after a tool call closes the provider's function-call
response. A later non-tool 'MESSAGE.END' is the terminal continuation for the
call. A provider error, close, caller cancellation, deadline, or rejected
result send is a terminal failure while the obligation is still incomplete.

The state is keyed by 'call_id', not by a counter. Parallel calls therefore
remain independent: accepting or completing one call cannot clear, duplicate,
or cross-attribute another call.

## State transitions

| State | Entry boundary | Required next boundary | Close or next-turn effect |
| --- | --- | --- | --- |
| Requested | A provider 'TOOLCALL.START', 'TOOLCALL.DELTA', or 'TOOLCALL.END' exposes a non-empty 'call_id'. | Execute the call and send its correlated result. | Blocks the current turn, scheduled dispatch, and automatic close. |
| Executing / queued | The tool executor returns or the loop assembles a tool result. | A successful provider-facing result send. | Still blocks; local completion is not delivery evidence. |
| Result accepted | 'SendWithOutcome' reports success for 'TOOLCALL.END', or the complete-message path reports success for the rich result. | One accepted 'RESPONSE.CREATE' continuation boundary. | The result is no longer undelivered, but it still blocks until continuation terminal. |
| Continuation requested | 'RESPONSE.CREATE' is accepted for the flat batch, or 'SendMessage' requests the response for a rich result. | A later non-tool assistant 'MESSAGE.END'. | Still blocks; provider acceptance alone cannot close or dispatch. |
| Continuation complete | The later non-tool 'MESSAGE.END' is observed after result acceptance and continuation request. | None for this call. | Releases the next scheduled input and may satisfy close eligibility. |
| Failed | Result send rejection, provider close/error, cancellation, or deadline before completion. | None; teardown reports the remaining IDs. | Non-zero terminal status with a phase-aware typed error; never clean success. |

For a flat text result, 'ToolResultForwarder' sends one correlated
'TOOLCALL.END' per result and one 'RESPONSE.CREATE' after the batch. For a rich
result, 'ModelRunner' uses the complete-message capability when available and
requests the response only after the result item (and any image item) is
accepted. A mixed batch is owned by one path so a text sibling cannot be sent
twice.

## Ownership and package boundaries

| Boundary | Owner | Contract |
| --- | --- | --- |
| Provider wire ↔ generic stream | 'go-llm-gateway/pkg/providers/openai' and 'grok' | Preserve the provider 'call_id', translate function-call events to 'TOOLCALL.*', and translate 'TOOLCALL.END' to the provider's correlated 'function_call_output'. |
| Tool result projection | 'go-agent-loop/pkg/subsystems/tool_result_forwarder.go' | Deliver each flat result once in call order and emit one explicit continuation control event for the batch. |
| Rich result projection | 'go-agent-loop/pkg/participants/model_runner.go' | Use complete-message sends where supported; retain the stream fallback and suppress unrelated user-text replay after a tool result. |
| Provider send acceptance | 'agent-cli/internal/services/session_live.go' ('observedSession') | Treat only a successful 'SendWithOutcome' / complete-message outcome as acceptance; retain the first rejected status by 'call_id'. |
| Lifecycle observer | 'agent-cli/internal/services/session_diagnostics.go' | Own the ID-keyed requested, accepted, continuation-requested, and continuation-terminal state, plus deterministic snapshots and diagnostics. |
| Live scheduling and close | 'session_live.go' | Run dispatch and close predicates on the serialized session-loop goroutine. Both predicates require no outstanding tool lifecycle obligation. |
| Duration admission and teardown | 'session_duration.go' and 'session_drain.go' | Apply the same gate to bounded runs, drain late deltas before terminal classification, and preserve primary provider/cancellation/deadline causes. |

The reusable 'go-agent-loop' and gateway packages do not decide when a
customer-facing session may close. They expose the typed stream and send
outcome boundaries; the CLI session service owns the cross-package lifecycle
observer because it also owns scheduled-input and terminal policy.

## Synchronization and wake-up rules

- 'sessionProgressObserver.toolStateMu' guards every lifecycle map and state
  transition. Provider delta consumption and provider-facing send acceptance
  may occur on different goroutines.
- 'toolLifecycleCh' is a capacity-one, coalescing notification. A notification
  means “re-evaluate the complete snapshot,” never “one call completed.” This
  keeps parallel calls and duplicate events idempotent.
- Result acceptance and continuation terminal observation both notify the
  channel. The live and duration runners select that channel, then call
  'dispatchScheduledInputs' before 'closePendingSessionIfReady' on the same
  session-loop goroutine. No provider delta or timer polling is required to
  release a completed next turn.
- A send acknowledgement can arrive before the consumer observes the
  provider's 'TOOLCALL.END'. The observer creates an ID placeholder and later
  enriches it, so the ordering does not lose the continuation obligation.
- Terminal paths briefly drain the loop outbox before stopping it. This lets a
  queued provider tool event or continuation boundary contribute to the final
  error instead of being mistaken for a clean close.

The shared predicates are therefore:

~~~
next scheduled input eligible = session ready
  AND prior scheduled turn complete
  AND no unresolved result
  AND no accepted result without a terminal continuation

automatic close eligible = every scheduled input committed
  AND every scheduled assistant turn complete
  AND no lifecycle obligation
~~~

No-tool sessions retain their existing terminal behavior. Tool-enabled
sessions that terminate early expose 'ErrSessionUnresolvedToolResults',
'ErrSessionToolContinuationIncomplete', or the more specific image
continuation error, with sorted affected IDs and the primary terminal cause
preserved through 'errors.Join'.

## Hermetic proof surface

The credential-free proof uses the real 'agent session' CLI over deterministic
session doubles and replay transports. The relevant checks cover:

- missing result delivery and accepted-result-without-continuation controls in
  'agent-cli/test/integration/session_tool_result_failure_test.go';
- scheduled next-turn ordering and the provider-wire barrier in
  'agent-cli/test/integration/session_tool_result_barge_in_test.go' and
  's2s_async_tool_result_interrupts_speech_test.go';
- duplicate, out-of-order, rejected-send, and continuation state transitions
  in 'agent-cli/internal/services/session_tool_lifecycle_test.go';
- provider serialization and correlated 'function_call_output' pairing in
  'go-llm-gateway/pkg/providers/openai' tests.

The negative controls must fail on a clean-but-wrong close, not merely on a
hang. They use channel barriers and observable provider events; wall-clock
sleeps and source-inventory assertions are not ordering evidence.

## Live OpenAI confirmation

This is an additional billed check, not a credential-bearing CI test. Run it
from the repository root on the final tested commit with OpenAI Realtime
access for 'gpt-realtime', macOS 'say', 'ffmpeg', 'ffprobe', and Python 3.
Keep all generated files in a private temporary directory. Never put the API
key in an argument, raw capture, log, committed fixture, or PR comment.

### Prepare the exact spoken inputs

~~~
make -C agent-cli build
AGENT_BIN="$PWD/agent-cli/bin/agent"
test -x "$AGENT_BIN"

umask 077
probe_root="$(mktemp -d /tmp/s2s-audio-tool.XXXXXX)"
read -r -s AGENT_MODEL__OPENAI__API_KEY
printf '\n'
export AGENT_MODEL__OPENAI__API_KEY

say -o "$probe_root/date-request.aiff" \
  "run a command that prints the current date and tell me the result"
ffmpeg -hide_banner -loglevel error -y \
  -i "$probe_root/date-request.aiff" -map 0:a:0 -vn \
  -ac 1 -ar 24000 -c:a pcm_s16le "$probe_root/date-request.wav"

probe_file="s2s-live-tool-probe.txt"
test ! -e "$probe_file"
probe_content="realtime lifecycle verified"
say -o "$probe_root/write-request.aiff" \
  "create a file named $probe_file containing exactly $probe_content"
say -o "$probe_root/read-request.aiff" \
  "read the file named $probe_file and tell me its exact contents"
for stem in write-request read-request; do
  ffmpeg -hide_banner -loglevel error -y \
    -i "$probe_root/$stem.aiff" -map 0:a:0 -vn \
    -ac 1 -ar 24000 -c:a pcm_s16le "$probe_root/$stem.wav"
done
~~~

The first audio file is the exact one-turn request. The second pair is the
two-turn write/read-back request. The relative 'probe_file' is intentionally
created in the repository working directory used to launch the CLI; assert it
after the run and remove that exact file during cleanup.

### Date probe

~~~
mkdir "$probe_root/date-recording"
"$AGENT_BIN" --config-dir "$probe_root/config" session \
  --provider openai --model gpt-realtime \
  --record "$probe_root/date.session.json" \
  --record-dir "$probe_root/date-recording" \
  --max-duration 120s \
  --system-prompt \
    "When the user asks to run a command, use the exec tool. For a current date request, run the date command now, then speak a brief answer containing the returned date. Never invent tool output." \
  --audio-in-turn "$probe_root/date-request.wav" \
  >"$probe_root/date.stdout" 2>"$probe_root/date.stderr"
~~~

The sanitized capture check must show exactly one provider function call to
'exec', exactly one outbound 'conversation.item.create' whose item type is
'function_call_output' and whose 'call_id' matches that request, and exactly
one continuation 'response.create' after the result. It must also show a later
'response.done', non-zero decoded response-audio bytes, and an output-audio
transcript containing the date returned by the real 'date' command. Record the
date value and transcript excerpt only after checking that they contain no
customer data.

### Two-turn write/read-back probe

~~~
mkdir "$probe_root/write-read-recording"
"$AGENT_BIN" --config-dir "$probe_root/config" session \
  --provider openai --model gpt-realtime \
  --record "$probe_root/write-read.session.json" \
  --record-dir "$probe_root/write-read-recording" \
  --max-duration 120s \
  --system-prompt \
    "Use write_file for the first spoken request and write exactly the requested content. Use read_file for the second spoken request. Do not start turn 2 until turn 1's tool continuation has completed. Speak the exact file content in turn 2." \
  --audio-in-turn "$probe_root/write-request.wav" \
  --audio-in-turn "$probe_root/read-request.wav" \
  >"$probe_root/write-read.stdout" 2>"$probe_root/write-read.stderr"

test -f "$probe_file"
test "$(tr -d '\n' < "$probe_file")" = "$probe_content"
~~~

The sanitized two-turn check must show one write tool call and one read tool
call, one matching 'function_call_output' per call, and one grounded
continuation after each result. The second turn's input append/commit must be
after the first continuation's terminal 'response.done'; both turns must have
non-zero output audio and completed recording entries. The turn-2 transcript
must contain 'realtime lifecycle verified', and the file assertion above must
pass.

### Evidence to record in the PR

Record only a short sanitized summary, not the raw capture or audio:

~~~
OpenAI live audio-tool lifecycle confirmation (<provider>/<model>, <UTC date>).
- Tested commit: <sha>; environment: <OS>, Go <version>, ffmpeg <version>.
- Date request: exec called once; result call_id matched; one function_call_output; one continuation response.create; terminal response.done; response audio <N> bytes; transcript contains returned date <date>; exit 0.
- Write/read request: write_file then read_file once each; two matched function_call_output events; turn 2 append followed turn 1 terminal response.done; output audio <N>/<N> bytes; transcript contains the expected content; file assertion passed; exit 0.
- No credentials, raw captures, customer audio, authorization headers, or unredacted logs included.
~~~

After the run, unset the key, remove the exact probe file created in the
working directory, and delete or securely retain the private temporary
directory according to local policy:

~~~
unset AGENT_MODEL__OPENAI__API_KEY
rm -f "$probe_file"
rm -rf "$probe_root"
~~~

If credentials or provider access are unavailable, report that exact blocker
and do not fill in passing counts. The hermetic tests above remain the
mandatory CI proof; the live check must be rerun by an operator with access
before claiming the live acceptance criterion.
