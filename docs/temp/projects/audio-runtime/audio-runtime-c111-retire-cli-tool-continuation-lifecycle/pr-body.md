## Summary

- Extracts unresolved tool-result and image/ordinary tool-continuation snapshot construction, normalization, metadata, and typed error joining into the public `go-agent-runtime/services/sessioncontinuation` contract with a private implementation and dedicated Wire package.
- Leaves the CLI lifecycle file as a 48-line Deprecated compatibility adapter; the immutable baseline was 147 lines, for 99 net physical production lines retired.
- Keeps compatibility identity at the public contract boundary and enriches an unresolved snapshot once, so one obligation produces one typed lifecycle error rather than a duplicate legacy join.
- Covers the legacy runtime error view from the extracted contract, including empty, populated, and annotation-free deterministic formatting paths, plus an explicit error-tree assertion for the single unresolved lifecycle error.
- Adds a credential-free external consumer, bounded causal/replay evidence, and coverage registrations for the new packages.
- Preserves sentinel and typed-error compatibility with the reusable live runtime while exposing the extracted public error types.
- Integrates freshly fetched current `origin/main` (`bd6a1289218d1bef1a3af36e64e9d4496062416f`) with no reset in merge `44747db8fca0a1f5e11ce9722b709976316b943a`; the final repair checkpoint is `1e85e5e340a084f5d0ff3a2e85449d6ff796001d`.

## Evidence

- Admission remains `admitted` for `audio-runtime` in `~default`; `prd.branchName` matches `codex/audio-runtime-c111-retire-cli-tool-continuation-lifecycle`, and startup, accepted-main, and current-main ancestry are preserved without resetting the host checkout.
- Exact C111 repair checkpoint: `1e85e5e340a084f5d0ff3a2e85449d6ff796001d`; verifier integrated-main pin: `bd6a1289218d1bef1a3af36e64e9d4496062416f`.
- `verify.py --mode retirement-and-owned-paths`: accepted; artifact `artifacts/verify-1789305549-14143.json` (SHA256 `80335d2e52d8a0185b819f14fff2aba0d8fcfaa1c9ee4d57b18b96f54322047f`). It verifies the 147-line immutable baseline/hash, 48-line adapter, 99 retired production lines, forbidden-path protection, and the admitted branch.
- `verify.py --mode positive-and-negative-controls`: accepted; artifact `artifacts/verify-1789305574-14161.json` (SHA256 `4a268e09b879cde19c0dfd1e6c8af3fe2eaec7ef0e5ae3b97a727a2b7ce7caed`). Runtime contract/race, GOWORK=off external consumer, and all four accumulated regressions exited 0, timed_out=false, and reaped=true.
- Source-pinned credential-free replay at `1e85e5e3`: accepted; artifact `artifacts/run-1789305724-20535.json` (SHA256 `22c6303f653dc9d278f6d92a2217f4c8a11b0e5e2a28bcaff43a26ef7cdccedb`). The YUI build exited 0 at 51,122,754 bytes (SHA256 `3a29a36dda5da23a4ff1f856dadbcbc4f77b165a453a114995454c25efde44a4`); missing-tool and corrupt-audio tests passed, tool replay returned `PROBE_TOOL_MARKER_9182`, non-tool replay completed, both audio outputs were produced, all process groups were reaped, and realtime sessions were 0.
- Focused post-repair tests pass: session-continuation race/error-tree checks (8 tests across 3 packages) and the four named agent-cli regressions (4 tests across 59 packages). Pinned staticcheck 2026.1, golangci-lint 2.9.0, fmt, coverage registration (188 packages across 6 modules), architecture-size-check (198 packages/1,928 files/28,650 functions), vet, Wire check (12 packages), and `git diff --check` all pass.
- The test-facing `servicetest` compatibility facade now projects the public continuation sentinels/types so the explicit `Deprecated:` markers remain lint-clean; no unrelated runtime or diagnostic files changed.
- Historical Script CI run `34729269748` is not claimed green for this repair. A fresh script CI gate submission is the next external action; CI was not polled or duplicated locally.

## Gate status

The prior independent findings are repaired: the CLI adapter no longer joins a second unresolved-result error, image/tool aliases carry explicit Go `Deprecated:` markers, and the verifier/evidence are refreshed to current `origin/main` and the exact repair checkpoint. No self-review was performed.

The candidate is ready for the script-owned CI gate on this same PR. Script CI is not claimed green; if it rejects, inspect the exact failed checks/logs, repair this same task, and resubmit before requesting a fresh independent review and guarded merge.
