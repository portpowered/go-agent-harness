# C109 current-head rejection and bounded revalidation — 2026-09-13

This checkpoint changes only the admitted C109 evidence directory. Candidate
source, C61/C83 predecessor worktrees and refs, C79-owned shared registries,
production/test source, the running host checkout and unrelated worktrees were
not changed.

Admission and ancestry:

- `project-control.py verify-work --type task --name audio-runtime-c109-characterize-browser-retirement-integration --root "$FACTORY_ROOT"` returned `admitted` for the sole project `audio-runtime`.
- The current branch is `codex/audio-runtime-c109-characterize-browser-retirement-integration`, exactly matching `prd.json.branchName`.
- `git fetch origin main` completed successfully; `origin/main` remains `3963bc3566da24f8214634c17a9d0f79a6724171`, already integrated and an ancestor of the candidate. Accepted main `d4766c3dbbf2c198142047ead4449d58dd47d485` and startup integration revision `8bdafc7f947a3a2c9856220abdc539437035bd21` remain ancestors.
- The exact C61 and C83 refs remain `8e8177c031a7b3b9322d712af19970e13fa7a1bc` and `22cc6769aaf06d1e2c1275b064cc7ec29de3e371`; their clean pinned predecessor worktrees remained present before and after verification.

Focused causal evidence from current source head `0185f03afbd2bf81e6d2cdb32d3a1586576aad7d`:

- `verify.py --mode all --write /tmp/audio-runtime-c109-verification-0185-current.json` passed. It covered both merge orders, source-derived API/caller/conflict ledger, attribution, handoff sequence, determinism, all 20 negative fixtures, caller-tree mutation rejection, and retained public reports. Output SHA-256: `ee6276463f1fa9b3900f727c910e85c21691a8f466223ad6786ee829f523b542`.
- The required browser/audio/tool command with `--child-timeout 90 --aggregate-timeout 300` passed all 18 checks. Every child stayed within its bound (maximum observed child `85.128s`), output was uncapped, discovery was positive for required Go checks, credential markers were absent, process groups were gone, and the exact synthetic tree remained unchanged. Output SHA-256: `ac16c5df31360215bc4567f814ce843595794822b6d754d19586661efbc132d8`.
- The malformed/canceled command with `--child-timeout 60 --aggregate-timeout 180` passed all 3 checks. Normal and race cancellation each discovered one test, output was uncapped, credential markers were absent and process groups were gone. Output SHA-256: `e5d1c8928620ea990d95a401268fdf071d488ea39a826f5a288aba4f6c9f12f3`.
- The exact inherited `test46/provider_burst` local control passed once in `15.617s`; it remains non-waiver evidence because the hosted failure is not reproducible locally.

Latest CI rejection:

- The complete retrieved job log for run `34732547786`, job `103657892852`, was read and retained during this dispatch. Its SHA-256 is `3ec0d3acb03405b376f2ef7a6e4fe32fbfdd968939811d9e2ae3fdb73dbc13a1`; the durable sanitized projection is `ci-rejection-34732547786.json`.
- Eight required lanes passed. `CI (integration)` alone failed in
  `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst` at line 183. The child was still running when the remote playback deadline expired: `rendered_pcm=464160`, `nonzero_pcm=150871`, `expected_pcm=174391`, `final_marker=false`, `callbacks=967`, zero queued/dropped/overflow/discarded samples, and a final render underflow trace. The deterministic integration and committed replay steps in that job passed.
- This is the known `C79/provider-audio` terminal-drain family, outside the C109 evidence-only lease. No provider/device/transport/integration/Wire/baseline repair was made or authorized, and the failure is not relabeled or waived.

Handoff state:

- No CI-green claim, independent review, merge, vertical probe, hardware/acoustic proof or project acceptance is made. C61 and C83 remain unmerged/unaccepted; all nine broad project gates remain `OPEN`.
- Exact next action: commit and push this owned evidence checkpoint, update PR #495 with the current-head rejection and bounded revalidation, and submit the changed same-task head to script CI without polling. Retain C109 ownership for any new in-scope evidence defect; C79 retains the provider-audio repair.
