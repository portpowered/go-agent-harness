# Concurrency issues

## AGENT LOOP

- we should return a wrapper buffer around the response object of being a channel, since we don't want to expose that detail to the user. 
- due to the way that ordering works in select, multiple channels there is no ordering, we need to basically merge the outboxes for each actor in the engine to a single outbox. 
- we need to be able to deterministically execute all the systems, so the runners and the buffers need to expose Tick() functions and we should be able to manually tick through the different components to confirm behavior at a timing level. 
- we should record the tick times and have an ordered tick timer as part of the system engine, then record them as part of the logs. 
- the response from the streaming response should not expose full events, and that should be removed
- we should document how the system works, along with how tick ordering works. 
- the input/output buffers should be configured as part of each subsystems execution request parameters, rather than having to wire in them manually. 
- we should have a single input buffer for each of the subsystems when possible. 
- we should have the loggers be stored within the context of the engine, so that we don't have to manually inject it all over the place. 
- the kernel writer has a bunch of writers that we don't want anymore, and should delete all of that. 
- we should seperate the ticks from the hot loop, and expose them as functions. 
- each of the runners should have their own hot loop and tick functions, and the engine should just call the hot loop function of each when in the hot loop. 
Otherwise, when running default tick mode, it should just take in the inputs and outputs. 
- we should add fully ordered index sorting of all the execution elements. 

## AGENT GATEWAY

## AGENT CLI 

- ✅ we should support passing through different log levels like --verbose, --v 2/3, etc. 
- we should support passing urls into the agent cli, such that they get parsed as data. 
i.e. when a customer pushes in a url of a youtube link, we should model it as a youtube. If the file has a well known reference extension to a file, we should model it appropriately. 

- ✅ code should be refactored such that there is an agent/ package, that contains the main construction of the agent loop and execution of it, rather than putting it in the ask.go service file. The constructor should receive a struct with well known input flags such as --system-prompt, etc. The ask service should transform CLI inputs into said parameters. 
- ✅ we should remove the AskRunner interface and MockAskRunner, and instead have the CLI commands directly use the agent.Executor package. The agent/ package should be the single source of truth for agent loop construction and execution.
- ✅ the chat and ask.go commands should be refactored such that they are just calling the agent/ package to execute the agent loop. 
- the chat command should have similar command parameters as the ask.go command

- ✅ we should log out the errors/failures of the CLI to a file, rather than to stdout, and we should configure the logger as such by default, but with a flag to override it as part of the CLI flags.

## Implementation notes

Every time you make a change to a project, ensure that it compiles. 
Namely, run `make build` and `make test` in the libraries/ directory of the project. 
we want the go-agent-loop, agent-cli, and go-llm-gateway to all compile and pass their tests. 

When done, mark the item as done as part of the NOTES.md file. 

## Flaky Tests

### TestBasic_StreamingWithChunks

`test/functional/basic_test.go` — `TestBasic_StreamingWithChunks`

**FIXED** by draining `modelRunner.DeltaOutbox` in the `case resp = <-modelOut` branch
of `readCurrentDataBuffer` (engine.go). When InferenceResponse arrives, all remaining
deltas are pulled from DeltaOutbox into `ModelInputDelta` in the same tick, so
CoordinatorDelta forwards them before LOOP.END is sent.

### TestRetryDelta_StreamNoDuplicateMessages

`test/functional/retry_delta_test.go` — `TestRetryDelta_StreamNoDuplicateMessages`

**ROOT CAUSE IDENTIFIED (2026-02-27):**

The `KernelRunner.modelTextIndexBuffer` retains stale entries from a failed inference
run. When a second (retry) inference run processes `TEXT.DELTA "Final answer."`, the
REWRITE path (`idx < modelTextWrittenUpTo`) merges indices `[modelTextMinIndex..idx]`
to build the RESET frame content. Because the buffer still holds the first run's `"par"`
entry at e.g. index 5, the merge produces `"par" + "Final answer."` = `"parFinal answer."`
as the RESET replacement content, which is wrong.

Debug evidence: raw stream bytes showed either:
- `"partial\xff\xff\x00\x01\x00\x00\x00\x10parFinal answer."` — RESET emitted but
  with wrong (stale-merged) content
- `"parFinal answer."` — no RESET because retry index >= modelTextWrittenUpTo

**FIX:** Add a `needsReset bool` field to `KernelRunner`. In `closeStreamWithError`,
if `modelTextWrittenUpTo > 0`, set `needsReset = true` and clear `modelTextIndexBuffer`
(nil). In the TEXT.DELTA handler, when `needsReset` is true, emit a RESET frame with
ONLY the new content (not merged with stale buffer data), then clear `needsReset`.
This ensures cross-run retries always emit a clean RESET.

**TerminateLoop persistence bug (FIXED):** `flushCurrentState` in `engine.go` did not
reset `state.LoopState.Inputs.TerminateLoop`. After Turn 1, `TerminateLoop=true`
persisted. Turn 2's first delta tick sent LOOP.END immediately (via CoordinatorDelta),
closing `messageOutCh` before any messages were collected. Fix: added
`state.LoopState.Inputs.TerminateLoop = false` to `flushCurrentState`.
