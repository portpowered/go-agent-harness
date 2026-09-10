## Summary

- Retire CLI image staging, extension selection, read_image path advertisement, and refresh decoration behind the public tools contract.
- Keep CLI host ConfigDir/home resolution and option mapping only; add the dedicated tools Wire constructor without changing generated Wire graphs.
- Keep filesystem ownership, permissions, cleanup, no-read_image no-op behavior, refresh behavior, public consumer proof, and CLI image/audio behavior covered.

## Admitted revision

- Sole admitted project: `audio-runtime`; task: `audio-runtime-c43-retire-cli-image-staging`.
- Branch: `codex/audio-runtime-c43-retire-cli-image-staging`, matching `prd.json` and the isolated worktree.
- Current candidate: `af73b8e8b54979a3738fa68e56e834c6e83fd5b7` (`test(c43): consolidate image staging coverage and register package`).
- Current-main integration is preserved as merge `ee1bb0af`, with fetched `origin/main=11b9035b`; the required startup and reviewed-base ancestors remain present.
- No architecture baseline, generated Wire file, peer task path, host checkout, waiver, or CI result was changed or claimed.

## Repairs

- Consolidated the two leased host adapter staging checks into the existing `session_image_test.go`, removed the extra leased test file, and kept the file at exactly 600 lines; the agentruntime package-file count is back at its unchanged baseline.
- Added the exact coverage registration for `go-agent-runtime/services/tools/internal/imagestaging` with the unchanged 80% floor.

## Gate evidence

- `make architecture-check size-check wire-check`: PASS; architecture/size inventory passed at 182 packages, 1,871 files, and 27,628 functions; Wire regeneration produced no diff.
- `make coverage-registration`: PASS; 173 workspace packages registered.
- `make coverage-changed COVERAGE_BASE=926ded7bfa8f3c3e42115192d03aa1240c4806db`: PASS.
- Focused normal and race image/read_image CLI tests: PASS (27 focused tests); private imagestaging normal/race and all `go-agent-runtime/services/tools/...` normal/race suites: PASS; affected package vet: PASS.
- Refreshed `verify.py --mode focused` run `runs/verify-20260910T175434Z-17458`: PASS. The public consumer read 70 literal PNG bytes with matching SHA-256 `4ff6ab670a58c14270e034e2090d9a432caa263a14e0a25785386b0c12f880b5`, preserved the exact refreshed path, and removed it idempotently; the wrong-oracle negative rejected 71 expected vs 70 actual bytes, and the post-cleanup negative rejected reads.
- The shipped CLI image/audio workflow and credential-free strict OpenAI tool replay both pass.

## Handoff

The candidate is implementation-complete and ready for the script-owned CI gate. This PR does not claim green CI, independent review, merge, or project acceptance. Submit this exact head to script CI; if CI rejects it, inspect the exact failed check/log, repair this same task, and resubmit.
