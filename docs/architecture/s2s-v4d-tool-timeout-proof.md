# Bounded tool-call timeout with graceful session degradation — proof (s2s v4d)

## Claim

A tool call whose executor never returns is bounded by an explicit, short
`ToolExecutionTimeout`; the deadline expiry surfaces as an observable outcome
on the session stream that names the timed-out tool; and the session degrades
gracefully — later provider turns cross, a subsequent fast tool call succeeds,
and the run terminates cleanly with no error.

The production mechanism under proof lives at the session adapter
(`agent-cli/internal/services/session_tools.go`,
`newSessionToolExecutorWithTimeout`): one invocation is wrapped in a deadline
context (default `defaultSessionToolExecutionTimeout` = 60s; zero override
selects it), and every expiry mode — including a worker that ignores context
cancellation — is converted into correlated tool-result content
(`tool %q failed: tool execution timed out`) with a nil Go error so the loop
never escalates a single hanging tool into a fatal session error.

## How v4d enforces it

The lane is proven by `agent-cli/test/integration/s2s_v4d_tool_timeout_test.go`
through the exported production entry point `services.RunSession`, which plans
the real duplex runtime (`session_runtime_plan.go`) and constructs the real
loop via `duplexSessionLoopOptions`
(`session_live.go:57` → `agentloop.WithToolExecutor(newSessionToolExecutorWithTimeout(...))`).
Nothing in the lane stubs or re-implements the bounding behavior.

- **Injected dependency** — `v4DHangingExecutor.Execute` blocks forever on
  `call_v4d_timeout_001` and deliberately ignores its `ctx`, so no cooperative
  cancellation can end the call: only the adapter deadline can. Every other
  invocation answers instantly.
- **Causal gating** — the scripted `SessionInferencer` releases the recovery
  turn only after the rendered stream contains
  `tool "get_weather" failed: tool execution timed out`, releases the second
  fast tool call after that turn crossed, and closes the session last. Ordering
  is therefore proven causally: the graceful-degradation evidence cannot appear
  unless the bounded timeout outcome appeared first.
- **Latency attribution** — the capturing writer timestamps the first chunk
  carrying the failure text. The positive assertion requires the outcome to
  cross within `[override−2ms, 1.5s]`, which excludes both the 60s default and
  any session-window cutoff as the effective bound.
- **Structured diagnostics** — the canonical `session_metrics` record must
  account for the post-timeout assistant text plus the second tool result,
  proving the traffic crossed before termination.

### Why not a raw WebSocket replay capture

The duplex runner terminates synchronously on the provider close delta while
tool batches route asynchronously, so a canned capture cannot create a causal
gate between deadline expiry and session teardown without racing it (the close
crossing cancels the engine before a 10–75ms batch can land). The injected
`SessionInferencer` is the established hermetic seam for internal-timing proofs
— identical in kind to the package-level `TestRunAgentLoopSession_*` table —
reached here from outside the package through the exported API. The replay
transport lanes (v2/v3) remain the owners of wire-format coverage.

### Production default wiring (verified against origin/main @ fe7f335)

- `services/session_live.go` — `duplexSessionLoopOptions` wraps the composed
  executor with `newSessionToolExecutorWithTimeout(opts.ToolExecutor,
  opts.ToolExecutionTimeout)`; this is the single live construction seam for
  plain and duration-bounded session runners.
- `services/session_options.go` — `SessionRunOptions.ToolExecutionTimeout`
  (added by this lane) threads the test override through
  `planSessionRuntimeWithFactory`; zero selects the documented 60s default and
  the CLI never sets it, so production plans are unchanged.
- `services/session_tools_test.go` — extended unit assertions prove, without
  waiting out the bound, that `newSessionToolExecutor` hands the inner executor
  a fresh ~60s deadline (`TestSessionToolExecutor_DefaultWrapperAppliesProductionBound`)
  and that the plan threads executor + override end-to-end while zero keeps the
  default path (`TestPlanSessionRuntimeThreadsToolExecutorAndDeadlineOverride`).

## Outcomes

| case | executor | override | expected result |
|---|---|---|---|
| positive | hangs on `call_v4d_timeout_001` | 25ms | clean nil-error run; correlated timeout names `get_weather`; recovery turn + succeeding `get_time` result cross; metrics account for all of it |
| `...RequiresExplicitOverride` | hangs | 0 (60s default) | hanging call exercised, but bounded outcome absent — proves the explicit override, not the default, produced the positive timing |
| `...SuppressedTimeoutOutcomeFailsNonVacuously` | always fast | 25ms | validator rejects the transcript naming its missing expectations |

Both controls fail non-vacuously: any regression that makes the suppressed or
default-bound run look like the positive path fails the suite with an explicit
"missing expected ..." message.

## Reproduction

```sh
# The proving integration lane (positive + two negative controls)
go test ./agent-cli/test/integration -run 'TestS2SV4D' -count=1 -v

# Default-wiring unit proofs (no 60s waits)
go test ./agent-cli/internal/services \
  -run 'TestSessionToolExecutor_DefaultWrapperAppliesProductionBound|TestPlanSessionRuntimeThreadsToolExecutorAndDeadlineOverride' \
  -count=1 -v
```

## Observed results (2026-08-26)

Positive run completes in ≈30ms wall clock; the timeout outcome crosses with a
measured latency inside `[23ms, 1.5s]` (configured override: 25ms), followed by
the recovery transcript, the succeeding `get_time` payload, the continuation
transcript, and `[session closed: fixture_complete]`. Both negative controls
pass by failing their suppressed expectations with explicit messages. Five
consecutive positive runs pass unchanged. Full suites:
`go test ./agent-cli/internal/services` green;
`golangci-lint run ./internal/services/... --new-from-rev origin/main` clean.

## Scope note

Provider-visible delivery of local tool results remains owned by the separate
v3b/v4a lanes: the duplex outbound translation forwards user audio/text and
cancels only (`go-llm-gateway/pkg/providers/grok/events.go translateOutbound`),
so the replayed provider exchange carries no client-side
`function_call_output`. This lane's contract — bounded completion, correlated
stream outcome, graceful degradation — is fully observable without it.
