# Phase 4 Provider Capabilities And Local Validation Contract

## Problem

`go-llm-gateway` exposes request fields for tools, streaming, sessions, audio,
image input, video output, reasoning, prompt caching, and provider-specific
config, but callers cannot query one public capability contract before issuing a
request. Provider differences are documented in prose and enforced
provider-by-provider, so unsupported feature failures can be late, string-only,
or provider-specific.

## Proposed Repair

Add a public capability discovery contract to the gateway/provider surface with
explicit `supported`, `unsupported`, and `unknown` states for:

- tools
- stateless streaming
- sessions
- audio input and audio output
- image input
- video output
- reasoning
- prompt caching
- provider-specific config

Then add local unsupported-feature validation before stateless and session
provider execution. Validation errors should be typed and inspectable, identify
the feature, provider, requested mode, and capability state, and preserve the
existing cancellation and timeout behavior.

## Evidence To Add

- public docs describing the capability vocabulary and provider matrix
- deterministic fake-provider tests proving unsupported stateless requests do
  not call the provider
- deterministic session-provider tests proving unsupported session requests do
  not connect
- concrete-provider tests proving published capability values match behavior
- audit row updates mapping the repair to `P4-API-04`, `P4-API-06`, and
  `P4-GATE-01`

