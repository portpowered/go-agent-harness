## Summary

- Extracts unresolved tool-result and image/ordinary tool-continuation snapshot construction, normalization, metadata, and typed error joining into the public `go-agent-runtime/services/sessioncontinuation` contract with a private implementation and dedicated Wire package.
- Leaves the CLI lifecycle file as a 57-line Deprecated compatibility adapter; the immutable baseline was 147 lines.
- Covers the legacy runtime error view from the extracted contract, including empty, populated, and annotation-free deterministic formatting paths.
- Adds a credential-free external consumer, bounded causal/replay evidence, and coverage registrations for the new packages.
- Preserves sentinel and typed-error compatibility with the reusable live runtime while exposing the extracted public error types.

## Evidence

- Exact implementation head: `6aa2442c062616074af3d15a6cd2cf51216e26d2` (implementation `b7eb303a` plus repairs `fc2a2e3` and `158983c0`, documentation-only provenance updates, and the compatibility regression).
- `verify.py --mode positive-and-negative-controls` from the exact head: accepted; artifact `artifacts/verify-1789260812-28873.json` (SHA256 `14c57e832c3a640c94d6beb4f1f69b6986596d4ba5c0723c0a177a53403952fc`).
- Bounded replay from exact head: accepted for missing continuation, corrupt audio delta, and non-tool audio; artifact `artifacts/run-1789260847-29785.json` (SHA256 `e8fd048b90a569f76f32a29c622030736ceaf5794d6e70020e50439cf677fdbd`), with `nomicrophone` yui SHA256 `8df94084bfbb194a476f0efe5d1987ab3d6903f649faa71c98af955f77e0f14a`.
- The exact failed shipped negative control now passes; the C111 verifier passes runtime contract/race, external `GOWORK=off` consumer, and all four accumulated regressions. Pinned golangci-lint and staticcheck report zero issues.
- Full `make coverage` passes with `go-agent-runtime/services/session` at 48/56 weighted statements (85.7%), after the compatibility regression restored the three legacy formatting paths identified by accepted-main comparison.

## Gate status

The previous PR #496 CI run rejected the pre-repair head because the shipped image-continuation error preserved text but not errors.As compatibility, and because of three owned lint findings; those are repaired in `fc2a2e3`. The follow-on `158983c0` keeps the public sentinels immutable while preserving legacy identity at the CLI boundary. The new candidate has been pushed, but Script CI has not been polled and is not claimed green. The candidate is waiting on C79 (`work-task-114`) to explicitly release the shared Wire registry and architecture baseline. `make wire-check` and `make architecture-size-check` each report only the expected unregistered `go-agent-runtime/services/sessioncontinuation/wire/wire_gen.go`; those shared paths remain unchanged. After that release, the same task should fetch current `origin/main`, preserve required ancestry, add only demonstrated shared registrations, rerun bounded gates, and resubmit for Script CI and independent review.
