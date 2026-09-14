## Summary

- Preserve room participant runtimes captured at bound start through the existing grace window.
- Force-phase cancellation now retains the active-response ownership needed to deliver exactly one cancellation after grace.
- Keep the provider-failure path authoritative and redacted.
- Restore the existing architecture baseline metrics without editing the shared baseline.

## Causal repair

The coordinator previously derived force-phase cancellation from the current active map. A response could retire from that map during the unchanged 40 ms grace, leaving the force phase unable to deliver its cancellation. The coordinator now retains the bound snapshot through grace; the bound-start lifecycle mark remains authoritative, and force-phase cancellation marks bound cancellation on those retained runtimes before invoking the existing cancellation path. No grace, deadline, timeout, failure precedence or redaction policy changed.

The owned bound-shutdown test synchronizes on an active response, asserts zero cancellation at bound start, and requires exactly one active cancellation after grace with zero peer cancellation. The duration fixture queues the provider handshake before connection readiness. Its test-only no-tick cadence removes an unrelated periodic mixer-silence admission race from this ownership oracle; production mixer construction/teardown remains exercised, while accumulated healthy audio/tool coverage uses normal cadence.

## Verification

- C154-01 normal count 50, coverpkg `CGO_ENABLED=0 -tags=nomicrophone` count 50, and race count 20: pass.
- C154-02 normal count 50, coverpkg count 20, and race count 20: pass.
- The rejected `TestRunRoom_MaxTurnsDrainsResponseAlreadyInFlight` regression passes in normal count 50, CGO-disabled count 50 and race count 20.
- Accumulated room/audio/tool/lifecycle/participant/terminal/`TrackedSession` normal count 3, race count 1, coverpkg count 1: pass.
- `go vet ./agent-cli/internal/services/internal/agentruntime` and `git diff --check`: clean.
- Post-repair `make architecture-size-check`, `make fmt`, `make wire-check`, pinned `make staticcheck` (`2026.1`) and pinned `make lint` (`v2.9.0`, 0 issues in all 15 modules): pass. The repair checkpoints are `855188771` and `2654c2bce`.
- Integrated candidate preserves startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, planning main `4a1c399ccbb3d780be95eb04316e84b8f11a6646`, and fetched review-time `origin/main` `2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`.

The accepted-main negative control reproduced the prior failure in a temporary detached worktree: the package-wide cover run had three duration subtests with zero active cancellations and finished at 8.0% coverage. Historical script CI run `34513957197` also recorded the C154 duration-zero failure; it is not current-head evidence.

The current task feedback for PR `#520` at head `6e80575de0e707da69bf9ab1f4e70668a66b579d` reported `CI (coverage)=FAILURE` ([log](https://github.com/portpowered/go-agent-harness/actions/runs/34790722251/job/103814302202)) and `CI (hermetic)=FAILURE` ([log](https://github.com/portpowered/go-agent-harness/actions/runs/34790722251/job/103814302190)). Both failed on `TestRunRoom_MaxTurnsDrainsResponseAlreadyInFlight`; the force-phase lifecycle re-mark overwrote the mid-response completion trigger after a grace-window completion. Repair commits `855188771` and `2654c2bce` remove that redundant re-mark while retaining the bound-start snapshot and existing cancellation path.

## Scope and gate status

The diff against `origin/main` contains only the two admitted Go paths plus this owned evidence directory. Final repair head is `2654c2bce7d9816b1213e6df54c4de77dcf6b78b`. C154 is a baseline causal slice, not C143 recovery or vertical/project acceptance. This head is ready for script-owned CI; this PR body does not claim CI green, independent review, guarded merge or later-gate completion.
