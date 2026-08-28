# Live OpenAI Realtime confirmation — speech barge-in proof v3

`TestLiveSessionS2SBargeInProofV3` is an explicitly opted-in, build-tagged
confirmation of the credential-free shipped-CLI barge-in proofs. It uses the
real OpenAI Realtime WebSocket session, `gpt-realtime`, four non-empty PCM
utterances, observer-gated boundaries, `--record`, `--record-dir`, and
`--audio-out`. The test owns all generated paths below a private `TempDir` and
discards command output while retaining only normalized counts and byte totals
for diagnostics.

The default integration suite does not compile or run this billed probe. A
missing credential, authentication/setup failure, rate limit, provider
unavailability, timeout, zero-turn run, or missed timing boundary is
**INCONCLUSIVE**, never a successful proof. A repeatable contract violation is
a failed test.

## Exact redacted-key protocol

Run from the repository root. The wrapper reads the key without printing it,
passes it to the in-process shipped `agent session` command, and unsets the
shell variable on normal, error, and signal paths:

```bash
KEY=$(tr -d '\n' < ~/.you-agent-factory/secrets/OPENAPI_API_KEY)
cleanup_key() { unset KEY; }
trap cleanup_key EXIT HUP INT TERM
probe_rc=0
OPENAI_API_KEY="$KEY" \
AGENT_HARNESS_LIVE_S2S_BARGE_IN_V3=1 \
go test -tags live ./agent-cli/test/integration \
  -run '^TestLiveSessionS2SBargeInProofV3$' -count=1 -timeout 2m -v || probe_rc=$?
unset KEY
trap - EXIT HUP INT TERM
exit "$probe_rc"
```

The test's effective shipped command selects `--provider openai`,
`--model gpt-realtime`, `--audio-in -`, `--audio-out`, `--record`,
`--record-dir`, and `--max-duration 75s`. The 90-second test context and
two-second command join bound are below the documented package budget. No key,
authorization header, raw provider payload, provider session ID, transcript,
capture, or customer audio belongs in the PR or repository.

## Boundaries and claim

The reader waits for `session.updated`, then paces the four fixture utterances
at the harness's 30 ms PCM frame cadence. It releases each next utterance only
after an observed provider-neutral boundary:

1. response R1 has emitted non-empty assistant audio, proving active-speech
   interruption;
2. response R2 has been created while its first output is still withheld,
   proving turn-start interruption; and
3. response R3 has completed before the final input, proving same-session
   continuation after both cancellations.

The private capture adapter correlates provider response identities and maps
them to ordered redacted labels. The ledger requires one non-empty append
group, commit, and user turn for each input; exactly one cancellation for R1
and R2; no output after either gated input; completed output-bearing R3/R4;
and a clean terminal with no unresolved work. Runtime observations and the
recording directory independently reconcile four committed inputs, four turn
boundaries, non-empty assistant output, and final accounting.

This live probe deliberately disables the default tool list in its temporary
config. It therefore does **not** claim that a real model-selected tool call
was timed against speech. The credential-free hermetic shipped-CLI proof in
`s2s_live_barge_in_tool_call_test.go` owns the outstanding-tool collision and
its delivered/rejected/cancelled accounting. Any sanitized replay of that
fixture is a replay of the hermetic contract, not a second live confirmation.

## Safe evidence shape

Successful output is limited to a normalized ledger of the following form;
the byte counts are evidence of non-empty payloads, not customer content:

```text
T1{append_group=1,commit=1,user_turn=1} R1{cancelled,audio_bytes=N,text_bytes=N};
T2{append_group=1,commit=1,user_turn=1} R2{cancelled,audio_bytes=0,text_bytes=0};
T3{append_group=1,commit=1,user_turn=1} R3{completed,audio_bytes=N,text_bytes=N};
T4{append_group=1,commit=1,user_turn=1} R4{completed,audio_bytes=N,text_bytes=N};
terminal={clean=true,unresolved=0}
```

The actual successful sanitized ledger and the exact command above are copied
verbatim into the pull-request description after the bounded live run.
