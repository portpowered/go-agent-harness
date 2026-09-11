# Audio runtime C54: room document admission

## Problem

Room JSON/YAML decoding, normalization, browser policy validation, credential-name admission, typed errors, and the public Wire provider must be owned by the runtime room service while the CLI remains a thin adapter.

## Candidate

- Same admitted task and PR: `audio-runtime-c54-room-document-admission`, #445.
- Candidate merge: `49266d2bf44bea734f647d88941e1578919f000f`.
- Required startup `8bdafc7f947a3a2c9856220abdc539437035bd21`, predecessor baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and fetched/current `origin/main=904e1f4c3be6c1e629138632573bd2fb55d50938` are ancestors.
- Generated `go-agent-runtime/services/rooms/wire/wire_gen.go` is unchanged.

## Validation

- Runtime rooms normal/race tests pass.
- CLI room normal/race tests pass.
- Direct `go-agent-runtime/services/rooms/wire` coverage is 100%.
- `make fmt`, `make wire-check`, `make coverage-registration`, focused room vet, and `git diff --check` pass.
- Public runtime files remain within the immutable 400-line budget.
- The clean pre-merge C54 evidence bundle passed all 10 bounded cases and final provenance verification at `00e5c46`.

## Known external gate

After the required current-main merge, the accumulated C54 runner reaches the unowned C47 package and fails `TestProductionWebMCPCLISelectsListedCompositeReference` in `agent-cli/internal/transport/cli`: the selected target does not provide WebMCP. `make architecture-size-check` independently reports the same current-main baseline drift, `agent-cli/internal/transport/cli` 148 lines versus baseline 147. These C47 paths and the baseline are preserved unchanged in this C54 candidate; the exact prior CI metadata is recorded under `docs/temp/projects/audio-runtime/audio-runtime-c54-room-document-admission/ci/` from run `34617011922`.

The next action is for the C47 owner to repair composite target selection and reconcile the baseline after an actual reduction, then rerun the script CI gate for this same C54 task. No CI success, independent review acceptance, merge, or project-wide completion is claimed here.
