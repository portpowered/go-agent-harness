## Summary

- Extracts unresolved tool-result and image/ordinary tool-continuation snapshot construction, normalization, metadata, and typed error joining into the public `go-agent-runtime/services/sessioncontinuation` contract with a private implementation and dedicated Wire package.
- Leaves the CLI lifecycle file as a 57-line Deprecated compatibility adapter; the immutable baseline was 147 lines.
- Covers the legacy runtime error view from the extracted contract, including empty, populated, and annotation-free deterministic formatting paths.
- Adds a credential-free external consumer, bounded causal/replay evidence, and coverage registrations for the new packages.
- Preserves sentinel and typed-error compatibility with the reusable live runtime while exposing the extracted public error types.

## Evidence

- Exact implementation/evidence head: `b5f6e422883397f5a3b07a3c80a583f0d8003fd8` (implementation `b7eb303a` plus repairs `fc2a2e3` and `158983c0`, compatibility coverage `6aa2442c`, and documentation-only provenance updates).
- `verify.py --mode positive-and-negative-controls` from the exact head: accepted; artifact `artifacts/verify-1789262697-68153.json` (SHA256 `1db547c8af9903ab3769af3257a55021df89e1c438e90a137fc7db8038fc4d05`).
- Bounded replay from the exact head: accepted for missing continuation, corrupt audio delta, and non-tool audio; artifact `artifacts/run-1789262735-68621.json` (SHA256 `1a2d294b18e2604f3409bf804be0d54f980b37f12164aab12d0f2f73421bd7d3`), with `nomicrophone` yui SHA256 `8df94084bfbb194a476f0efe5d1987ab3d6903f649faa71c98af955f77e0f14a`.
- The exact formerly failing shipped negative control passes; the C111 verifier passes runtime contract/race, external `GOWORK=off` consumer, and all four accumulated regressions. Pinned golangci-lint and staticcheck report zero issues.
- Full `make coverage` passes with `go-agent-runtime/services/session` at 48/56 weighted statements (85.7%), after the compatibility regression restored the three legacy formatting paths identified by accepted-main comparison.
- The exact current Script CI run `34729269748` passes unit, race, integration, coverage, hermetic, WebMCP Chrome, macOS audio software, and Windows portable software. Its only failures are the expected C79-owned generated Wire registration and architecture generated-file-spoof check; Script CI is not claimed green.
- The prior coverage-floor failure (`76.80%` versus unchanged `80.00%` for `go-agent-runtime/services/session`) is resolved on the current head, and the bounded `test46/provider_burst` characterization passes without demonstrating a C111 cause; details are in `bounded-causal-characterization-b5f6e422.md`.

## Gate status

The previous PR #496 CI run rejected the pre-repair head because the shipped image-continuation error preserved text but not errors.As compatibility, and because of three owned lint findings; those are repaired in `fc2a2e3`. The follow-on `158983c0` keeps the public sentinels immutable while preserving legacy identity at the CLI boundary. The current candidate is pushed and its latest Script CI run is recorded above; it is not claimed green because C79 still owns the shared generated-Wire registry and architecture baseline. `make wire-check` and `make architecture-size-check` each report only the expected unregistered `go-agent-runtime/services/sessioncontinuation/wire/wire_gen.go`; those shared paths remain unchanged. After C79's reviewed guarded merge explicitly releases them, the same task must fetch and merge current `origin/main` without reset, apply only demonstrated shared deltas, rerun bounded gates, and resubmit the changed head for Script CI and independent review.
