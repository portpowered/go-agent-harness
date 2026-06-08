# Phase 4 Provider Capabilities And Local Validation Contract

## Problem

`go-llm-gateway` now exposes a starter provider capability contract and local
unsupported-feature validation. Callers can query supported, unsupported, and
unknown states through gateway/provider APIs, and explicitly unsupported
stateless/session requests can fail before provider execution. The remaining
work is to reconcile stale audit evidence, complete concrete provider coverage,
prove all relevant public seams, and document edge cases such as unknown
fallbacks and fal streaming behavior.

## Proposed Repair

Keep the public capability vocabulary and validate the remaining coverage for:

- tools
- stateless streaming
- sessions
- audio input and audio output
- image input
- video output
- reasoning
- prompt caching
- provider-specific config

Then reconcile gateway-level unsupported-feature validation across stateless,
streaming, session, interaction, and inferencer seams. Validation errors should
remain typed and inspectable, identify the feature, provider, requested mode,
and capability state, preserve cancellation and timeout behavior, and prove that
providers are not called when an explicitly unsupported capability rejects the
request locally.

## Evidence To Add

- reconciled audit rows mapping the implemented capability APIs to `P4-API-04`,
  `P4-API-06`, and `P4-GATE-01`
- public docs and examples describing the capability vocabulary, provider
  matrix, unknown fallback behavior, and unsupported-feature error contract
- deterministic fake-provider tests proving unsupported stateless and streaming
  requests do not call the provider
- deterministic session-provider tests proving unsupported session requests do
  not connect
- concrete-provider tests proving published capability values match behavior,
  including known unsupported provider-specific modes such as fal streaming
