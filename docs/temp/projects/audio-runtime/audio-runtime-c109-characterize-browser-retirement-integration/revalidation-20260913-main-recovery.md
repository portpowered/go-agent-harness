# C109 current-main recovery revalidation — 2026-09-13

This checkpoint retains the same admitted task and changes only the C109
evidence directory.  The running host checkout, preserved C61/C83 refs and
worktrees, production/test source, shared registries, and unrelated worktrees
were not changed.

Admission and integration:

- `project-control.py verify-work --type task --name
  audio-runtime-c109-characterize-browser-retirement-integration --root
  "$FACTORY_ROOT"` returned `admitted` for project `audio-runtime`; the
  isolated branch exactly matches `prd.json.branchName`.
- `git fetch origin main` resolved review-time `origin/main` to
  `bd6a1289218d1bef1a3af36e64e9d4496062416f`.  It was merged into the isolated
  branch as `76d12b2304bad77be5e7f9ab0af5e0d44e8260ec`; accepted main remains
  `d4766c3dbbf2c198142047ead4449d58dd47d485`, startup integration remains
  `8bdafc7f947a3a2c9856220abdc539437035bd21`, C61 remains
  `8e8177c031a7b3b9322d712af19970e13fa7a1bc`, and C83 remains
  `22cc6769aaf06d1e2c1275b064cc7ec29de3e371`.
- The current branch diff against `origin/main` contains only the admitted
  C109 evidence directory.  The final JSON provenance records exact source,
  tree, ref, blob, preserved-worktree and cleanup identities.

Focused evidence:

- Required `main -> C61 -> C83` and reverse control `main -> C83 -> C61`
  analyzer runs pass with unchanged candidate refs/worktrees.  The four
  analyzer regressions pass, and `verify.py --mode merge-orders` passes.
- The formerly rejected hermetic tests pass `10/10` with
  `CGO_ENABLED=0 -tags=nomicrophone`:
  `TestRunRoomWithResult_BidirectionalOverlapRecordsPeerOnlyEvidence` and
  `TestRunSessionWithAudioOut_FinalizesPlayableWAV`.
- The bounded credential-free browser/audio/tool public case passes `18/18`
  under `--child-timeout 90 --aggregate-timeout 300`; malformed/canceled
  passes `3/3` under `60/180`.  The final reports retain positive discovery,
  credential scrubbing, observable local effects, bounded cleanup and no
  surviving process groups.
- `verify.py --mode all --write runs/final/verification.json` passes all eight
  checks, deterministic analyzer reruns, 19 negative fixtures, caller-tree
  mutation rejection, attribution and public-report binding.  Exact hashes
  are retained in the final machine-readable bundle.

This remains evidence-only.  Hosted CI has not been polled or claimed green;
C61/C83 remain unmerged, unaccepted and unprobed; software replay is not
device/acoustic proof; and all nine broad project gates remain open.  The next
action is to commit the final regenerated C109 evidence, push the same branch,
update PR #495, and submit the exact head to the script CI gate without
polling; retain the same task for any exact in-scope evidence rejection.
