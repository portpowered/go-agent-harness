## Summary

- Extracts unresolved tool-result and image/ordinary tool-continuation snapshot construction, normalization, metadata, and typed error joining into the public `go-agent-runtime/services/sessioncontinuation` contract with a private implementation and dedicated Wire package.
- Leaves the CLI lifecycle file as a 57-line Deprecated compatibility adapter; the immutable baseline was 147 lines.
- Adds a credential-free external consumer, bounded causal/replay evidence, and coverage registrations for the new packages.
- Preserves sentinel and typed-error compatibility with the reusable live runtime while exposing the extracted public error types.

## Evidence

- Implementation revisions: `b7eb303a8ce9498f44404e8d0baf61c111c43245` plus repair `fc2a2e30473337503dfd57c5623b351a042f21e6`.
- `verify.py --mode positive-and-negative-controls`: accepted; artifact `artifacts/verify-1789249249-88640.json` (SHA256 `abbade9bbd931b9c4254210be96409ed4818c6fce2b3bfff49fb516971705a8d`).
- Bounded replay: accepted for missing continuation, corrupt audio delta, and non-tool audio; artifact `artifacts/run-1789249286-88888.json` (SHA256 `f5951c0e856257a917f292cd98908c9c33d92ddc8f42ef372c794486e0c37fc3`).
- The exact failed shipped negative control now passes; the C111 verifier passes runtime contract/race, external `GOWORK=off` consumer, and all four accumulated regressions. Pinned golangci-lint and staticcheck report zero issues.

## Gate status

The previous PR #496 CI run rejected the pre-repair head because the shipped image-continuation error preserved text but not errors.As compatibility, and because of three owned lint findings; those are repaired in `fc2a2e3`. The new candidate has been pushed, but Script CI has not been polled and is not claimed green. The candidate is waiting on C79 (`work-task-114`) to explicitly release the shared Wire registry and architecture baseline. `make wire-check` currently reports only the expected unregistered `go-agent-runtime/services/sessioncontinuation/wire/wire_gen.go`; those shared paths remain unchanged. After that release, the same task should fetch current `origin/main`, preserve required ancestry, add only demonstrated shared registrations, rerun bounded gates, and resubmit for Script CI and independent review.
