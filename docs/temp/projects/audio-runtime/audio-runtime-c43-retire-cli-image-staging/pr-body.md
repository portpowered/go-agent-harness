## Summary

- Retire CLI image staging, extension selection, read_image path advertisement, and refresh decoration behind the public tools contract.
- Keep CLI host ConfigDir/home resolution and option mapping only; add the dedicated tools Wire constructor without changing generated Wire graphs.
- Keep filesystem ownership, permissions, cleanup, no-read_image no-op behavior, refresh behavior, public consumer proof, and CLI image/audio behavior covered.

## Admitted revision

- Sole admitted project: `audio-runtime`; task: `audio-runtime-c43-retire-cli-image-staging`.
- Branch: `codex/audio-runtime-c43-retire-cli-image-staging`, matching `prd.json` and the isolated worktree.
- Current candidate source is `a14b86638a7e1ba8b9d7f4438e97a1f6764c34b2` (`fix(c43): retain image staging cleanup errors`).
- Current-main integration is preserved as merge `3dfd88d3`, with fetched `origin/main=d6efc88d10e046396e777762802f5f731c9c6d9d`; the required startup and reviewed-base ancestors remain present.
- No architecture baseline, generated Wire file, peer task path, host checkout, waiver, or CI result was changed or claimed.

## Repairs

- Consolidated the two leased host adapter staging checks into the existing `session_image_test.go`, removed the extra leased test file, and kept the file at exactly 600 lines; the agentruntime package-file count is back at its unchanged baseline.
- Added the exact coverage registration for `go-agent-runtime/services/tools/internal/imagestaging` with the unchanged 80% floor.
- Repaired the current-head static rejection: the CLI adapter now handles and logs cleanup failures, private partial-write cleanup joins the write and cleanup causes, permission modes are named constants, and a regression proves both causes are retained.

## Gate evidence

- `make fmt`, `make wire-check`, `make architecture-size-check`, `make coverage-registration`, affected-package vet, pinned `make lint` v2.9.0 and pinned `make staticcheck` 2026.1: PASS; architecture/size inventory passed at 182 packages, 1,881 files, and 27,712 functions; 172 workspace packages are registered.
- Focused normal and race image/read_image CLI tests: PASS (22 tests in the affected agentruntime package); private imagestaging normal/race and all `go-agent-runtime/services/tools/...` regression suites: PASS; the new cleanup-cause regression is included.
- Refreshed `verify.py --mode focused` run `runs/verify-20260910T182006Z-42109`: PASS. The public consumer read 70 literal PNG bytes with matching SHA-256 `4ff6ab670a58c14270e034e2090d9a432caa263a14e0a25785386b0c12f880b5`, preserved the exact refreshed path, and removed it idempotently; the wrong-oracle negative rejected 71 expected vs 70 actual bytes, and the post-cleanup negative rejected reads.
- The shipped CLI image/audio workflow and credential-free strict OpenAI tool replay both pass.

## Handoff

The candidate is implementation-complete and ready for the script-owned CI gate. This PR does not claim green CI, independent review, merge, or project acceptance. Submit exact head `a14b86638a7e1ba8b9d7f4438e97a1f6764c34b2` to script CI; if CI rejects it, inspect the exact failed check/log, repair this same task, and resubmit.
