## Summary

- Extracts unresolved tool-result and image/ordinary tool-continuation snapshot construction, normalization, metadata, and typed error joining into the public `go-agent-runtime/services/sessioncontinuation` contract with a private implementation and dedicated Wire package.
- Leaves the CLI lifecycle file as a 57-line Deprecated compatibility adapter; the immutable baseline was 147 lines.
- Covers the legacy runtime error view from the extracted contract, including empty, populated, and annotation-free deterministic formatting paths.
- Adds a credential-free external consumer, bounded causal/replay evidence, and coverage registrations for the new packages.
- Preserves sentinel and typed-error compatibility with the reusable live runtime while exposing the extracted public error types.
- Integrates reviewed current `origin/main` (`1a8467246c6607a06ffc7289075da2595724ce8b`) without reset; the only post-integration shared validation delta is the demonstrated C111 generated Wire registration in `docs/architecture/architecture-policy.json`.

## Evidence

- Exact implementation checkpoint: `ea515cb356047e7b014247f44cfc1cddc90ff6a5` (current-main integration merge `a160181c9` plus the C111 registration repair).
- `verify.py --mode positive-and-negative-controls` from the exact checkpoint: accepted; artifact `artifacts/verify-1789288344-38539.json` (SHA256 `2c7dd41c406fa55e6a06e3afad43d54446919ea561596030e701b988400f7c04`).
- Fresh credential-free shipped replay from the exact checkpoint: accepted for missing continuation, corrupt audio delta, and non-tool audio; artifact `artifacts/run-1789289041-64019.json` (SHA256 `d3669b82ebd29c6fc26baa87d2b40922f21f6d9308ce5770e26724f274d98a04`), yui SHA256 `ae4134643ce24b2bf23c9ae96e0167ab532aca8fb281eca9df84d7d48015ec1f`, build exit 0, zero realtime sessions, and all process groups reaped.
- The four named C111 regressions and both causal negatives pass. `COUNT=1 bash scripts/test-session-ci-regressions.sh all` passes normal, coverage, and race lanes, including high-rate provider-burst 20/20. Pinned golangci-lint and staticcheck report zero issues.
- `make coverage` passes after integration with 182 registered packages across 7 profiles and no floor changes; `make architecture-size-check` reports 192 packages, 1,912 files, and 28,230 functions; `make wire-check` passes 10 discovered Wire packages; `make build`, `make vet`, and `git diff --check` pass.
- The historical Script CI run `34729269748` is not claimed green: its pre-integration static/generated-file-spoof failure is resolved locally by the exact C111 policy registration. A fresh script CI gate submission is the next external action.
- The prior coverage-floor failure and bounded `test46/provider_burst` characterization remain recorded in `bounded-causal-characterization-b5f6e422.md`; no C111-owned cause was demonstrated.

## Gate status

The previous PR #496 findings were repaired in the predecessor checkpoints. Current `origin/main` is integrated without reset, and the only C111-owned post-integration repair is the exact generated-file registration required for `go-agent-runtime/services/sessioncontinuation/wire/wire_gen.go`; shared registry and architecture baselines remain unchanged. The candidate is ready to push/update PR #496 and submit the same task to the script CI gate. Script CI is not claimed green; any rejection must be handled by inspecting its exact checks/logs, repairing this same task, and resubmitting before independent review and guarded merge.
