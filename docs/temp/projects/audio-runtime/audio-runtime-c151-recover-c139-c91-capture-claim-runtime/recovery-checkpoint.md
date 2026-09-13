# C151 recovery checkpoint

- Project/contract: `audio-runtime` / `audio-runtime-v1`.
- Admission: `project-control.py verify-work --type task --name audio-runtime-c151-recover-c139-c91-capture-claim-runtime --root "$FACTORY_ROOT"` returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c151-recover-c139-c91-capture-claim-runtime"}`.
- The admitted `prd.json` branch name is `codex/audio-runtime-c151-recover-c139-c91-capture-claim-runtime`, matching this isolated worktree and branch.
- Candidate `db94f83ff5e0a3afc66e96f8c878a7b35f311430` is a non-rewriting merge with current `origin/main` `4a1c399cc` as first parent and preserved C139 head `16f073183e3b61ccd8c401af4fdd6eb2e8440440` as second parent. The preserved startup ancestor `8bdafc7f` and C139/C91 ancestry are reachable.
- The separate C139 peer worktree remains at `16f073183`, clean, on `codex/audio-runtime-c139-recover-c91-capture-claim-runtime`. PR514 remains open at that preserved remote head; no review row or review finding exists. The prior script-CI run `34772244215` had eight successful lanes and one static rejection.
- The exact static rejection is four shared findings: three stale `acquireSessionRecordingClaim` baseline entries (cognitive complexity, cyclomatic complexity, function lines) and one unregistered generated `services/captureclaim/wire/wire_gen.go`. The shared registry and baseline files remain unchanged while the C110/C119 leases and explicit smallest-path transfer are unresolved.
- Focused causal evidence on the merged candidate: captureclaim normal `81` passes in three packages at `-count=3`; captureclaim race `54` passes in three packages at `-count=3`; CLI normal `99` passes at `-count=3`; CLI race `26` passes; C91 verifier `--mode all` passed; and the `GOWORK=off` external consumer passed.
- Accumulated `COUNT=1 bash scripts/test-session-ci-regressions.sh all` passed in normal, coverage, and race modes. `gofmt`, `go vet`, Wire check, coverage registration (`195` workspace packages), complete changed-coverage validation, lint, staticcheck, and `git diff --check` passed. The architecture gate reproduces only the four documented shared findings.
- No host checkout, predecessor worktree, predecessor PR, or shared registry/baseline file was reset, merged, rewritten, or otherwise modified. No CI handoff or green-CI claim has been made.

Next action: recheck the admitted board and exact C110/C119 lease state. Once those owners release the paths and the smallest explicit transfer is recorded, fetch `origin/main` again, merge it non-rewriting, change exactly the one captureclaim Wire registration and three stale baseline entries, rerun the focused and accumulated gates, commit, and push the same implementation to PR514's existing C139 head branch. Submit that changed head once to script CI without polling; if CI rejects, inspect the exact failed checks and repair/resubmit the same task.

## C151 verifier namespace repair and fresh bounded evidence

- The inherited C91 verifier's fail-closed scope allowlist omitted the admitted
  C151 evidence directory. The minimal owned-path repair added that one prefix
  and changed no runtime code, predecessor branch, shared registry, or
  architecture baseline.
- On the preserved candidate, `verify.py --mode all` now passes. Fresh focused
  service normal/race checks pass 105 tests in three packages each; focused CLI
  normal checks pass 99 tests and CLI race checks pass 26 tests; the isolated
  `GOWORK=off` consumer passes. `COUNT=1` accumulated session regressions pass
  in normal, coverage, and race modes, including their expected negative
  diagnostics.
- `make wire-check` regenerates stable output for all registered packages. The
  architecture gate remains fail-closed with exactly the four documented shared
  findings: three stale `acquireSessionRecordingClaim` baseline entries and
  the unregistered captureclaim `wire_gen.go`. No shared file was edited and no
  threshold was raised.

Next action remains unchanged: obtain the primary's explicit smallest-path
ownership transfer after the C110/C119 shared leases release, fetch and merge
the then-current `origin/main`, apply only the one captureclaim Wire registry
entry and three downward baseline deletions, rerun clean gates, then push PR514
and submit the changed head once to script CI without polling.
