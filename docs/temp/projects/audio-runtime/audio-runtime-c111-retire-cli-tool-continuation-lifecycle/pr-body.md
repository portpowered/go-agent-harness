## Summary

- Extracts unresolved tool-result and image/ordinary tool-continuation snapshot construction, normalization, metadata, and typed error joining into the public `go-agent-runtime/services/sessioncontinuation` contract with a private implementation and dedicated Wire package.
- Leaves the CLI lifecycle file as a 57-line Deprecated compatibility adapter; the immutable baseline was 147 lines.
- Adds a credential-free external consumer, bounded causal/replay evidence, and coverage registrations for the new packages.
- Preserves sentinel and typed-error compatibility with the reusable live runtime while exposing the extracted public error types.

## Evidence

- Exact implementation head: `1ccde3e5947850879205536f510676a8d6a2f13b` (implementation `b7eb303a` plus repairs `fc2a2e3` and `158983c0`, with documentation-only provenance updates).
- `verify.py --mode positive-and-negative-controls` from the exact head: accepted; artifact `artifacts/verify-1789253356-7580.json` (SHA256 `9161a2c24bbb79d8caff7006fee47ab74543d0b59ee0f0b771cac856b6b46dca`).
- Bounded replay from exact head: accepted for missing continuation, corrupt audio delta, and non-tool audio; artifact `artifacts/run-1789253424-10590.json` (SHA256 `6289994ffccb300d7ab5881845bae06059424b3937afaf9fbbbeadb7b602838e`), with `nomicrophone` yui SHA256 `8df94084bfbb194a476f0efe5d1987ab3d6903f649faa71c98af955f77e0f14a`.
- The exact failed shipped negative control now passes; the C111 verifier passes runtime contract/race, external `GOWORK=off` consumer, and all four accumulated regressions. Pinned golangci-lint and staticcheck report zero issues.

## Gate status

The previous PR #496 CI run rejected the pre-repair head because the shipped image-continuation error preserved text but not errors.As compatibility, and because of three owned lint findings; those are repaired in `fc2a2e3`. The follow-on `158983c0` keeps the public sentinels immutable while preserving legacy identity at the CLI boundary. The new candidate has been pushed, but Script CI has not been polled and is not claimed green. The candidate is waiting on C79 (`work-task-114`) to explicitly release the shared Wire registry and architecture baseline. `make wire-check` and `make architecture-size-check` each report only the expected unregistered `go-agent-runtime/services/sessioncontinuation/wire/wire_gen.go`; those shared paths remain unchanged. After that release, the same task should fetch current `origin/main`, preserve required ancestry, add only demonstrated shared registrations, rerun bounded gates, and resubmit for Script CI and independent review.
