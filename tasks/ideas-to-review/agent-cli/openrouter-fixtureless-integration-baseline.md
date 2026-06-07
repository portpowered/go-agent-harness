# Agent CLI integration baseline should not require live OpenRouter credentials

## Problem

Running `go test ./...` in `agent-cli` currently fails in many integration tests because the default ask/chat paths still select the OpenRouter provider and require `AGENT_MODEL__OPENROUTER__API_KEY`. This makes full module verification non-deterministic and repeatedly forces story work to fall back to narrower test slices.

## Why it matters

- This is a repeated mergeability blocker for unrelated changes.
- It weakens confidence in `agent-cli` CI because the default local test contract depends on external credentials instead of committed fixtures or explicit fakes.
- It makes it harder to tell whether a branch is failing for story-specific reasons or because of the shared baseline.

## Suggested direction

- Audit the failing ask/chat integration coverage and classify each case as fixture-backed, fake-provider-backed, or explicitly credentialed live coverage.
- Make the default `go test ./...` path deterministic without live OpenRouter credentials.
- Keep any truly live-provider coverage opt-in behind explicit environment gates so module-wide checks stay hermetic.
