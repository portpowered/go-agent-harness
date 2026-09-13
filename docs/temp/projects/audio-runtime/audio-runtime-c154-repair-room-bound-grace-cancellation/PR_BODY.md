## Summary

- Preserve room participant runtimes captured at bound start through the existing grace window.
- Force-phase cancellation now retains the active-response ownership needed to deliver exactly one cancellation after grace.
- Keep the provider-failure path authoritative and redacted.
- Restore the existing architecture baseline metrics without editing the shared baseline.

## Causal repair

The coordinator previously derived force-phase cancellation from the current active map. A response could retire from that map during the unchanged 40 ms grace, leaving the force phase unable to deliver its cancellation. The coordinator now retains the bound snapshot and re-marks those lifecycles at force before invoking the existing cancellation path. No grace, deadline, timeout, failure precedence or redaction policy changed.

The owned bound-shutdown test synchronizes on an active response, asserts zero cancellation at bound start, and requires exactly one active cancellation after grace with zero peer cancellation. The duration fixture queues the provider handshake before connection readiness. Its test-only no-tick cadence removes an unrelated periodic mixer-silence admission race from this ownership oracle; production mixer construction/teardown remains exercised, while accumulated healthy audio/tool coverage uses normal cadence.

## Verification

- C154-01 normal count 50, coverpkg `CGO_ENABLED=0 -tags=nomicrophone` count 50, and race count 20: pass.
- C154-02 normal count 50, coverpkg count 20, and race count 20: pass.
- Accumulated room/audio/tool/lifecycle/participant/terminal/`TrackedSession` normal count 3, race count 1, coverpkg count 1: pass.
- `go vet ./agent-cli/internal/services/internal/agentruntime` and `git diff --check`: clean.
- Post-repair `make architecture-size-check`, `make fmt`, `make wire-check`, pinned `make staticcheck` (`2026.1`) and pinned `make lint` (`v2.9.0`, 0 issues in all 15 modules): pass. The exact repair checkpoint is `6c91ba2`.
- Integrated candidate preserves startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, planning main `4a1c399ccbb3d780be95eb04316e84b8f11a6646`, and fetched review-time `origin/main` `2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`.

The accepted-main negative control reproduced the prior failure in a temporary detached worktree: the package-wide cover run had three duration subtests with zero active cancellations and finished at 8.0% coverage. Historical script CI run `34513957197` also recorded the C154 duration-zero failure; it is not current-head evidence.

## Scope and gate status

The diff against `origin/main` contains only the two admitted Go paths plus this owned evidence directory. C154 is a baseline causal slice, not C143 recovery or vertical/project acceptance. The exact post-repair head is ready for script-owned CI; this PR body does not claim CI green, independent review, guarded merge or later-gate completion.
