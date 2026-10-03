# C109 review-31 repair and upstream residual — 2026-09-13

This checkpoint remains inside the admitted C109 evidence directory. It does
not modify production or test source, the PRD or manifest, C61/C83 preserved
worktrees, C79 shared registries, or the running host checkout.

## Review repairs

- `run_public_checks.py` rejects an explicit `--output` path resolved inside a
  caller-owned synthetic tree before any report write. The caller-tree cleanup
  and identity contract therefore cannot be bypassed by the output artifact.
- `verify.py` now derives the public report contract from `command_set` and
  requires the exact case-specific labels, commands, order, and count. The
  `incomplete-public-report` negative fixture and focused regression prove that
  a report retaining only one child is rejected.
- The two short platform race controls run before the long integration stress
  controls. This is an order-only scheduler repair; command arguments,
  patterns, bounds, assertions, and ownership are unchanged.

Repair commits:

- `1aeb539bc0fa59e04c7b93e8fb8d925fce3016e1`
  (`fix(c109): fail closed on incomplete public evidence`)
- `6fa916c263778d9e8d8e21e669b62875181ef58b`
  (`fix(c109): schedule platform checks before stress`)

## Causal validation

- `test_analyze.py -v` passes all 7 focused regressions, including the
  caller-tree output-path, exact public-check-set, platform-order, and
  surviving-process-group controls.
- Required and reverse-order analyzer rehearsals pass against accepted main
  `d4766c3dbbf2c198142047ead4449d58dd47d485`, C61
  `8e8177c031a7b3b9322d712af19970e13fa7a1bc`, and C83
  `22cc6769aaf06d1e2c1275b064cc7ec29de3e371`; candidate refs and preserved
  worktrees remain unchanged. `origin/main` is still
  `bd6a1289218d1bef1a3af36e64e9d4496062416f` after the latest fetch.
- The malformed/canceled public matrix passes 3/3 in 37.107s under the
  admitted 60/180-second child/aggregate bounds, with clean process groups and
  temporary-tree cleanup.
- The refreshed positive matrix retains all 18 declared checks and cleanly
  reaps its temporary tree, but fails at 301.694s under the admitted
  90/300-second bounds. The first failure is the accumulated normal regression
  at `TestAgentBinaryTest46HighRateToolAudioRegression/trial_17`: the remote
  device rendered `167991/174391` samples, losing exactly 6400 while playback
  reports zero dropped, overflow, or discarded samples. The later race stress
  and lifecycle controls then hit their bounded child/aggregate time because
  the positive matrix is intentionally fail-closed.

This is the known provider-audio terminal-drain residual assigned outside C109
(C79/provider-audio; the prepared C127 repair is not yet in `origin/main`). C109
does not edit, duplicate, skip, waive, or relabel that failure.

## Exact next action

Push the two repair commits and this checkpoint to the existing PR #495, but do
not submit a knowingly failing positive matrix to script CI. After the
reviewed/guarded upstream provider-audio repair reaches `origin/main`, fetch
and integrate that accepted main into this same task, regenerate both analyzer
orders and both public matrices, run `verify.py --mode all`, then commit/push
the coherent evidence and submit the changed PR #495 head to script CI without
polling. Keep C109 evidence-only; C61/C83 acceptance, merge, hardware/acoustic
proof, and the remaining broad gates stay external.
