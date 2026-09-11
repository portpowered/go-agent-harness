# Audio runtime C54: room document admission

## Problem

Room JSON/YAML decoding, normalization, browser policy validation, credential-name admission, typed errors, and the public Wire provider must be owned by the runtime room service while the CLI remains a thin adapter.

## Candidate

- Same admitted task and PR: `audio-runtime-c54-room-document-admission`, #445.
- Candidate checkpoint: `cc740a131a0763a6325f20bb3d34195494fc6700`, integrating the accepted current mainline.
- Required startup `8bdafc7f947a3a2c9856220abdc539437035bd21`, predecessor baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and fetched/current `origin/main=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06` are ancestors.
- Generated `go-agent-runtime/services/rooms/wire/wire_gen.go` is unchanged.

## Validation

- Runtime rooms normal/race tests pass.
- CLI room normal/race tests pass.
- Direct `go-agent-runtime/services/rooms/wire` coverage is 100%.
- `make fmt`, `make wire-check`, `make coverage-registration`, focused room vet, and `git diff --check` pass.
- Public runtime files remain within the immutable 400-line budget.
- The exact merged-head C54 runner passed all 10 bounded cases at `cc740a13`, including the external GOWORK=off consumer and its four mutation oracles, shipped YUI room admission, credential-free audio/tool replay, focused normal/race controls, and bounded timeout/output cleanup. Refreshed `verification-summary.json` and `provenance.json` bind the candidate to `cc740a13` and `origin/main=d5d6f843`.

## Current handoff

The accepted mainline already contains the reviewed C47/C54/C55 composition and its transport, lifecycle, and architecture repairs. The merged candidate passes `make verify-architecture`, `make size-check`, `make coverage-registration`, C54 source formatting, focused room vet, and the accumulated session/replay regression matrix in normal, coverage, and race modes at `COUNT=3`. No script-CI result, independent review of this evidence-only successor, guarded merge of PR #445, vertical acceptance, or project-wide completion is claimed here.

The next action is to push this exact refreshed candidate to PR #445 and submit the same admitted task to the script CI gate without polling. Any exact current-head rejection remains with this task; later immutable executable probing and scoped acceptance remain primary-owned.
