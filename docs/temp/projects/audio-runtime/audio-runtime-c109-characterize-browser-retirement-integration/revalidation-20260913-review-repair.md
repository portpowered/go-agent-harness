# C109 review repair and current-main integration — 2026-09-13

Independent review `work-review-37` returned the same admitted task with two
actionable findings: the positive and negative public commands in `prd.json`
and the prior revalidation omitted the runner's required `--output`; and the
checked-in evidence was stale because review-time `origin/main` had advanced.
The repair stayed within the C109 evidence directory; no PRD, manifest,
production/test source, C61/C83 branch, preserved worktree, C79 registry or
architecture baseline was changed.

Admission and ancestry:

- `project-control.py verify-work --type task --name
  audio-runtime-c109-characterize-browser-retirement-integration --root
  "$FACTORY_ROOT"` returned `{"status":"admitted","project":"audio-runtime",
  "name":"audio-runtime-c109-characterize-browser-retirement-integration"}`.
- `prd.json.branchName` and the isolated branch are
  `codex/audio-runtime-c109-characterize-browser-retirement-integration`.
- Accepted main is `d4766c3dbbf2c198142047ead4449d58dd47d485`; fetched review
  main is `1a8467246c6607a06ffc7289075da2595724ce8b` and is integrated through
  merge commit `0c93c4ce0ba30599d7ae92fdf7b81cc0b5f2bcae`. Startup integration
  revision remains `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- Preserved C61/C83 revisions remain
  `8e8177c031a7b3b9322d712af19970e13fa7a1bc` and
  `22cc6769aaf06d1e2c1275b064cc7ec29de3e371`; their worktrees were clean at
  those exact heads before and after all analyzer/public runs.
- Final C109 source HEAD is `b9f6f0742131a78618df8bfcfe93eb8b78461364`.
  `git diff --name-only origin/main...HEAD` contains only the owned C109
  evidence directory.

Repair and focused evidence:

- `run_public_checks.py` now accepts the declared commands without `--output`;
  explicit `--output` still writes the retained JSON report. The new
  `test_analyze.py` regression invokes the no-output declaration and proves it
  reaches fail-closed tree validation rather than argparse failure.
- `test_analyze.py -v` passes `3/3` tests.
- Required and reverse-order analyzer rehearsals pass independently. Both
  retain complete source-derived API/caller/conflict ledgers, review-main
  compatibility evidence, unchanged preserved refs/worktrees and detached
  cleanup. Required/control merge-order outputs are byte-stable where the
  order-independent fields should match; order-specific merge/report hashes
  are retained separately.
- The explicit positive public case passes `18/18` in `275.452s` under the
  `90/300s` child/aggregate bounds. It discovers the browserrunner and
  browserscenario normal/race suites, both GOWORK=off consumers, the
  nomicrophone cross-module cancellation control, accepted-main delta barrier,
  and accumulated session normal/coverage/race regression groups. The
  malformed/canceled case passes `3/3` in `30.409s` under `60/180s` bounds.
  Both reports are credential-free, software-only, tree-bound, and cleanly
  reaped.
- `verify.py --mode all --write runs/final/verification.json` passes all eight
  checks and 19 negative fixtures, including stale-SHA, reversed-order,
  wrong-owner, forbidden-claim, missing-conflict, public-tree, zero-discovery,
  truncated-output, cleanup, and caller-tree mutation controls. Verification
  SHA-256 is
  `c99e255602ed6c81d8dc31b33767337ef4a918b818dff2ce10f7a4b83a5aaccb`.

Failure ownership and handoff:

- The prior hosted `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio /
  test46/provider_burst` terminal-drain failure remains preserved in
  `ci-rejection-34737204831.json` under C79/provider-audio ownership. C109 did
  not edit, duplicate, waive or relabel it. The unchanged WebMCP cleanup and
  agent-loop cancellation findings remain external inherited failures where
  recorded; local controls do not waive hosted CI.
- This is executor evidence only. C61/C83 remain unmerged, unaccepted and
  unprobed; software replay is not device/acoustic proof; AUDIO, DEVICE, EMBED,
  SERVICE, TRACE, REPLAY, FAILURES, QUALITY and PARITY remain open.
- Exact next action: commit and push this owned evidence checkpoint, update PR
  `#495`, and submit the exact current head to script-owned CI without polling.
  Retain this same task for any exact in-scope evidence rejection; independent
  review, guarded merge and the later C109 vertical probe remain external.
