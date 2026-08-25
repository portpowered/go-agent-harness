# s2s v5b — named toolset subset lease disposition

Status: **lease-exhausted; CLI proof not yet runnable on `origin/main`** (2026-08-25).

This lane is scoped to `agent-cli/test/integration/**`,
`docs/architecture/**`, and additive audio under
`go-agent-loop/testdata/audio/**`. The requested proof cannot be implemented
inside that lease because the shipped CLI/runtime contract it would exercise
is not present on the base commit.

## Missing shipped contract

The requested command shape is an actual CLI invocation with a named subset,
for example:

```text
agent session --replay <T1-capture>.json --tools get_weather
```

On `origin/main`:

- `agent-cli/internal/cli/session.go` exposes `--record`, `--replay`, prompt,
  audio, provider, model, and duration flags, but no `--tools` flag.
- `agent-cli/internal/cli/probe.go` exposes scenario/replay/output flags, but
  no tool-subset input or tool-call execution boundary.
- `agent-cli/internal/services/session_live.go` constructs the duplex loop
  with a session inferencer but no `WithTools`/`WithToolExecutor` path.
- There is no shipped typed subset-refusal event/code that carries the
  excluded tool name or call ID while guaranteeing no execution or side
  effect.

Adding those flags, definitions, executor composition, and refusal semantics
requires production changes under `agent-cli/internal/**`, which are outside
this lane. PR #83 (`s2s-b3-session-tool-executor-wiring`) is an open upstream
dependency for part of the missing session plumbing, but it does not by itself
provide the named `--tools` CLI boundary.

## Required follow-up contract

After the production surface exists, an in-lease integration test should run
the real `agent session` or `agent probe run` command over a committed T1
replay, reusing `tool_request_16k.wav` or `tool_request_24k.wav` if audio is
needed. It must assert an included call/result pair, a typed excluded-call
refusal with identity, zero excluded execution/side effect, bounded completion,
and a control run differing only by adding the excluded tool to `--tools`.

This document intentionally records no CLI-level v5b success claim and no
coverage claim for other verticals, live providers, or internal-only tests.
