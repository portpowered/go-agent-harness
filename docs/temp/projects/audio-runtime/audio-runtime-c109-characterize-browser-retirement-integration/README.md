# C109 browser-retirement integration characterization (v2 evidence)

This directory is the complete owned surface for `audio-runtime-c109-characterize-browser-retirement-integration`.
It is evidence-only.  The analyzer makes disposable detached worktrees, records
the C61/C83 provenance and merge-order behavior, and removes those worktrees
before returning.  It never edits either predecessor worktree, the host
checkout, shared registries, or candidate source files.

The accepted main revision is `d4766c3dbbf2c198142047ead4449d58dd47d485`.
The preserved candidate revisions are C61
`8e8177c031a7b3b9322d712af19970e13fa7a1bc` and C83
`22cc6769aaf06d1e2c1275b064cc7ec29de3e371`.
The final provenance records the freshly fetched `origin/main` as the newer
integrated review main; the latest revalidation uses
`4a1c399ccbb3d780be95eb04316e84b8f11a6646` and the branch diff relative to
that review main remains C109-owned only. This includes the accepted C127
repair mainline after its independent vertical repro; C109 does not duplicate
its provider-audio repair.
The required and control synthetic rehearsals intentionally remain based on
the admitted accepted main, as required by the PRD.  Because the newer review
main deletes the architecture baseline that C61 still modifies, the final
reports also retain a supplemental current-main compatibility rehearsal: it
records the exact modify/delete conflict and abort/cleanup result without
resolving or promoting it.

Run the analyzer from the repository root, writing outside the checkout while
iterating:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/analyze.py \
  --main d4766c3dbbf2c198142047ead4449d58dd47d485 \
  --c61 8e8177c031a7b3b9322d712af19970e13fa7a1bc \
  --c83 22cc6769aaf06d1e2c1275b064cc7ec29de3e371 \
  --order c61-c83 --output-dir /tmp/c109-required
```

The reverse control is run with `--order c83-c61 --expect-control`.  The
verifier accepts the two output directories and deliberately fails closed for
changed SHAs, reversed sequence ownership, unowned paths, missing merge
evidence, and prohibited candidate-acceptance claims.  `run_public_checks.py`
is the bounded software-only test runner; it records command, timeout,
credential-scrub, process-group, test-discovery, shipped-report, and
effect-cleanup evidence without claiming hardware or acoustic coverage. The
default child/aggregate bounds are 90/300 seconds; accumulated race coverage
is split into bounded transport, integration, devices, and OpenAI children
without relaxing the checked-in test patterns. A caller-provided `--tree`
must be the exact clean main -> C61 -> C83 synthetic tree and is rejected if a
child mutates it.

The C79-owned `scripts/wire-packages.txt` and
`docs/architecture/architecture-size-baseline.json` remain untouched.  The
6,400-sample integration loss remains assigned to C79/provider audio and is
not duplicated, repaired, or relabeled here.  C61 and C83 remain unmerged and
unaccepted until their own task/review/CI gates are resolved.

## Latest current-main recovery

After review-63 required a current-main refresh, the executor fetched
`origin/main=4a1c399ccbb3d780be95eb04316e84b8f11a6646` and integrated it into
the preserved C109 branch as merge commit
`1e2441a986203aae311dda0cef496eefc8b2b990` (parents
`db2b2ce48d0f7bb9477a4e92769b990ea3254157` and
`4a1c399ccbb3d780be95eb04316e84b8f11a6646`). The immutable accepted-main,
C61, and C83 inputs remain `d4766c3d`, `8e8177c0`, and `22cc6769`; the
preserved candidate refs and worktrees remained unchanged, and the startup
integration revision remains `8bdafc7f`.

Required and reverse-order analyzer rehearsals pass against the exact pinned
inputs and bind to review main `4a1c399c`; the full verifier passes all eight
checks and 20 negative fixtures. The fresh credential-free positive public
matrix passes 18/18 in `279.779s` under `90/300` seconds; the malformed or
canceled negative matrix passes 3/3 in `42.608s` under `60/180` seconds.
Both reports preserve clean synthetic trees, credential scrubbing, observable
software effects, and process-group cleanup. Final report hashes are retained
in `runs/final`; the final verification SHA-256 is
`03d30f46ee5e3c7ebeb9668f5023e4cbc4f23e4e121d3adae3e542ce41fe07e1`.

This remains executor evidence only: the new head has not been submitted to
script CI yet, and no CI-green, review, guarded merge, C61/C83 acceptance,
vertical probe, hardware/acoustic proof, or project-completion claim is made.

## C127 accepted-main dependency recovery

The C109 branch integrated fetched `origin/main=915ed982d23f2e549e529ff43c4f370b4b51e394`
as merge commit `ca618fe68ef181b2c75ccf7efaada0cf2b328760` after the C127
reviewed/guarded delivery and C138 independent vertical repro released the
provider-audio dependency. The exact prior PR #495 rejection is retained in
`ci-rejection-34762923229.json`: run `34762923229`, job `103738782236`, tested
head `64613f656f642b26bc09f670a3f677239ac74364`, had eight passing lanes and
failed only `test48_matched_healthy_control/provider_burst` with
`114395/120795` rendered samples, exactly 6,400 lost, zero dropped/overflow/
discarded samples, and 19 underflow events. The raw run metadata and complete
failed-job log were read and retained by SHA-256 in that record.

Fresh C109-owned evidence was then regenerated from the exact accepted-main
synthetic tree: the required and reverse analyzer rehearsals passed, the
credential-free browser/audio/tool matrix passed `18/18` under `90/300`
seconds, the malformed/canceled matrix passed `3/3` under `60/180` seconds,
and `verify.py --mode all` passed all eight checks plus 20 negative fixtures.
This remains software-only executor evidence; current-head script CI, fresh
review, guarded merge and the C109 vertical probe remain external, and C61,
C83, all nine broad gates, and hardware/acoustic proof are not claimed.

The checked-in final bundle includes separate `ci-attribution.json` evidence,
source-derived caller/API spans, exact committed-head provenance, both merge
orders, and the two public reports.  `verify.py --mode all` is the accumulated
gate for determinism, report/public negative controls, causal attribution, and
handoff readiness; it does not claim that candidate CI is green or that the
project is accepted.

## Current-main recovery revalidation

After the previously recorded hermetic rejection, `git fetch origin main`
resolved `origin/main` to `bd6a1289218d1bef1a3af36e64e9d4496062416f`, which was
integrated into this isolated branch as `76d12b2304bad77be5e7f9ab0af5e0d44e8260ec`.
The accepted main, startup integration revision, and immutable C61/C83 inputs
remain ancestral, and the branch diff against current `origin/main` remains
inside this directory.

The exact former hermetic failures
`TestRunRoomWithResult_BidirectionalOverlapRecordsPeerOnlyEvidence` and
`TestRunSessionWithAudioOut_FinalizesPlayableWAV` pass 10/10 under
`CGO_ENABLED=0 -tags=nomicrophone`.  Fresh analyzer orders, analyzer unit
regressions, both bounded public cases, and `verify.py --mode all` pass; the
machine-readable final bundle records the exact source, tree, ref, report and
negative-fixture hashes.  The public evidence remains credential-free and
software-only, and no hosted-CI, C61/C83 merge, acceptance, vertical, hardware
or acoustic claim is made.

The analyzer resolves API references from complete package/module identity:
anonymous function bodies do not become declarations, qualified imports are
matched to their exact imported package, same-name symbols in another package
are not treated as dependencies, and public module paths are not duplicated.
The checked-in `test_analyze.py` regressions cover those cases plus the admitted
public-command argument contract.  The final evidence source checkpoint is
`5eb90be6eeafeb5ea63e3e192ccd0544eed7da0c`.

## Review repair and current-main integration

Independent review `work-review-37` found two actionable evidence defects:
the admitted public commands omitted the runner's required `--output`, and
the evidence did not contain the newer review-time `origin/main`.  Commit
`0c93c4ce0ba30599d7ae92fdf7b81cc0b5f2bcae` integrates review main without
rewriting the C109 history; `b9f6f0742131a78618df8bfcfe93eb8b78461364` makes
`--output` optional for the declared commands and adds a regression test.  An
explicit output path still retains the JSON report used by the final bundle.

Fresh required/control analyzer outputs both record accepted main
`d4766c3dbbf2c198142047ead4449d58dd47d485`, review main
`1a8467246c6607a06ffc7289075da2595724ce8b`, source HEAD `b9f6f074`, unchanged
C61/C83 refs/worktrees, and complete cleanup.  The required output hashes are
`provenance 2e4bcecbe0a5c7e274f816464aae73d9dcfd53e43f6d11379cfc2d4d0347b686`,
`ledger 44dcb4c42c53f52ff7544eba9939436822960d2153aeee79e1e88ac9efa79209`,
`merge-orders 5d10f96910d69786a578097c14a7d88b561a2fc679712c89c682fadf75affd44`,
`report b769eb304057d4f97d59e1a8f6abf59187ada15dbdd4cc9207460808c569d01d`,
and `run-manifest 4690e2cef53541b4e588fcf7a2800d95a76ce0c055f1f0e5a3544fe47044c3c8`.
The control merge/report/manifest hashes are
`774be9b078a622fb82ccd0652685034f5638894ebdeddc1bc2c8b5a269fe071a`,
`cbfab1f02266996c56eb8559cda54f15c477b60fc0134f4da811450530393614`, and
`c0aab79c9b60e0d45e3f40d2396b9492a38ea96eb2f3dd805e39c16629d1a562`.

The repaired public runner passes the explicit positive case `18/18` in
`275.452s` and malformed/canceled case `3/3` in `30.409s`; their retained
report hashes are `0d367f5921101c52e98cf1308f6ec96f2d384429db4b02c388e77838542e6412`
and `d70da2bafbe2f2f5f87a46b31a2581b24a74060f25ab0e9e3d50b66ca9546e40`.
`verify.py --mode all` passes all eight checks and 19 negative fixtures; its
retained verification hash is
`c99e255602ed6c81d8dc31b33767337ef4a918b818dff2ce10f7a4b83a5aaccb`.
The prior hosted provider-audio terminal-drain rejection remains preserved
under C79 ownership and is not repaired or relabeled here.  C61/C83 and all
nine broad project gates remain open.

## Aggregate-budget runner repair

The first fresh current-source public run after the latest CI rejection passed
17/18 checks but exhausted the 300-second aggregate budget before the shipped
credential-free workflow could finish: all preceding checks passed, the final
child received only four seconds, terminated cleanly, and left no descendants.
This was an owned evidence-runner scheduling defect, not a C61/C83 or provider
audio failure. Commit `34a4a9404e1238b840c83c7acf7b93c14fa1abca` schedules the
required shipped workflow first without changing its command, assertions,
credential scrubbing, child timeout, or aggregate timeout.

The repaired runner passes the exact positive case 18/18 in 265.785 seconds
under 90/300 bounds and the malformed/canceled case 3/3 in 35.286 seconds under
60/180 bounds. The regenerated final public reports are bound to runner SHA-256
`04161bdc2d5f838b9af54b58c6b95f37fb5e1c515cffbb0693908b194fcc694a`, and the
final verifier passes all eight checks and 19 negative controls. This remains
software-only evidence; no C61/C83 merge, review, probe, acceptance, hardware,
acoustic proof, or broad project gate is claimed.

## Current script-CI rejection

The changed C109 head `a2fc31af4f59c2aa8b7c3ee6d4e61b6231c597c2` was checked by
PR #495 run `34731461393`. Coverage, hermetic, unit, static, race, WebMCP,
macOS, and Windows passed. Integration failed only in
`TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`
at `agent-cli/test/integration/session_tool_audio_remote_e2e_test.go:183`:
the remote playback deadline expired with `462720` rendered samples,
`174391` expected samples, `final_marker=false`, zero queued/dropped/overflow/
discarded samples, and the child still running. The exact sanitized record is
`ci-rejection-34731461393.json`.

This is the existing C79/provider-audio terminal-drain ownership boundary, not
a C109 evidence defect. The exact local control passed once in `15.867s`, but
that does not relabel or waive the CI failure. C109 did not change production,
integration, device, transport, Wire-registry, or architecture-baseline paths;
the changed evidence checkpoint is to be pushed and submitted to script CI,
while C79 retains the necessary source repair.

## Latest script-CI rejection

The exact current-head rejection for PR #495 is run `34732547786`, job
`103657892852`, at head `0185f03afbd2bf81e6d2cdb32d3a1586576aad7d`. Eight
other required lanes passed. Integration failed only in
`TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`
at `agent-cli/test/integration/session_tool_audio_remote_e2e_test.go:183`:
the remote playback deadline expired with `464160` rendered samples,
`150871` nonzero samples, `174391` expected samples, `final_marker=false`,
`967` callbacks, an underflow trace, zero queued/dropped/overflow/discarded
samples, and the child still running at timeout. The complete sanitized
record is `ci-rejection-34732547786.json`; the retrieved job log SHA-256 is
`3ec0d3acb03405b376f2ef7a6e4fe32fbfdd968939811d9e2ae3fdb73dbc13a1`.

The exact same local control passed in `15.617s`; this is preserved as a
non-waiver only. The failure remains the C79/provider-audio terminal-drain
family, with no C109 repair authorization. The current C109 candidate stays
evidence-only and must be resubmitted to script CI after this evidence
checkpoint; C61, C83 and all nine project gates remain unmerged/open.

## Review repair checkpoint — current main and bounded cleanup

The next independent review returned the same admitted task because live
`origin/main` had advanced to `09c70f51243caeaf1184c4806b99bbf7749e3044`, and
the evidence runner had three fail-closed defects: conflict abort results and
post-abort cleanliness were not retained, caller-owned trees were not reported
on exception, and `communicate()` captured unbounded child output before the
2 MiB cap. Merge commit `4f913fca8a` integrates that review main into the
isolated branch without resetting the host checkout or touching peer paths.

Commit `5eb90be6eeafeb5ea63e3e192ccd0544eed7da0c` repairs the owned analyzer,
public runner, and verifier. Child output is streamed with incremental
credential-marker scanning and full-stream hashing; a bounded control observed
2,097,153 bytes, retained 16 KiB, failed closed, and reaped the process group.
The caller-owned exception control preserved the pre-existing host files and
recorded `identity_unchanged: true`. Current-main compatibility rehearsals
record modify/delete conflicts with successful `git merge --abort` and empty
post-abort status.

Fresh required and reverse analyzer runs pass with unchanged C61/C83 refs and
predecessor worktrees. `verify.py --mode all` passes all eight checks,
determinism, 19 negative fixtures, caller-tree mutation control, and both public
reports. The current public positive report is 18/18 in 268.871s under 90/300
seconds; malformed/canceled is 3/3 in 29.092s under 60/180 seconds. Final
hashes are verification `428b51023f41139de5286101084533980de95af8ad768728f8f1b58c111c8f89`,
positive `ae9e8e718d50457b15892281d9dfe6c0d1ca2942e7141eb39ae6ba7bc46efbe5`,
and negative `29d86a3d783de76a04558560c26208c9a6908820f9eb0b3aa60b58bb678431d2`.
This remains evidence-only: no CI-green, review, merge, vertical probe,
candidate acceptance, hardware/acoustic proof, or project completion claim is
made.

## Final exact-head causal revalidation

The exact source-bound runner at `5eb90be6eeafeb5ea63e3e192ccd0544eed7da0c`
was revalidated from current executor HEAD `8f918bbf055fe1e96b1291fb7ede8225b9cf3838`.
`origin/main` remains `09c70f51243caeaf1184c4806b99bbf7749e3044`; accepted main,
startup integration, and the immutable C61/C83 refs remain unchanged and
ancestral. The worktree stayed clean and the preserved C61/C83 worktrees stayed
at their pinned heads.

`test_analyze.py -v` passes 3/3. `verify.py --mode all` passes all eight
checks, determinism, caller-tree mutation control, and all 19 negative
fixtures; its exact output remains
`428b51023f41139de5286101084533980de95af8ad768728f8f1b58c111c8f89`.
The fresh credential-free browser/audio/tool report passes 18/18 in 263.082s
under 90/300 seconds with clean process groups; its SHA-256 is
`dffa6d16e3febd78d45310bcea295c53b10873fbae5500954170304644176da5`.
The fresh malformed/canceled report passes 3/3 in 35.361s under 60/180
seconds with clean process groups; its SHA-256 is
`5de8e764d1db19a698738a1758354c0260fe192aaa7af5167454cc09432b6cf1`.

The prior hosted CI failures remain external, exact, and unwaived: the
WebMCP TempDir cleanup and agent-loop cancellation-isolation failures from run
`34737204831`, plus the C79/provider-audio terminal-drain history. C109 made no
peer-source or shared-registry change. This checkpoint still claims no
CI-green result, candidate acceptance, C61/C83 fix/merge/probe, hardware or
acoustic proof, or project completion. The next action is to push this exact
owned evidence head, update PR #495, and return it to script CI without polling.

## Surviving process-group cleanup repair

The latest independent review reproduced a cleanup defect in `bounded()`: an
exiting leader could close stdout while a silent descendant kept the process
group alive, and pipe EOF skipped cleanup because the leader had already exited.
The same admitted task repaired this in `run_public_checks.py` and added
`PublicRunnerCleanupTests.test_cleanup_reaps_surviving_group_after_leader_closes_stdout`.

The pre-repair runner at `8d8cfa68` returned `status=failed`,
`timed_out=false`, `process_group_gone=false`, and both `term_sent` and
`kill_sent` false for that control. The control harness then killed the
intentionally retained group and verified it was gone. The repaired runner at
`c3680d7` returns `status=timeout`, `timed_out=true`, sends TERM and KILL as
needed, records `group_gone_after_cleanup=true`, and returns
`process_group_gone=true`.

The committed source HEAD is `a8409ebc` after integrating current
`origin/main=071b0abf`. Focused `test_analyze.py` passes 4/4. Fresh positive
public evidence passes 18/18 in 254.711s under 90/300 seconds; malformed or
canceled evidence passes 3/3 in 31.847s under 60/180 seconds. Both reports
are credential-free, tree-bound, and have clean process groups.
`verify.py --mode all` passes all eight checks and all 19 negative fixtures.
The updated runner, test, verification, positive-report and negative-report
SHA-256 values are retained in the final bundle and source bindings.

This remains software-only evidence. It does not claim C61/C83 repair, merge,
review, acceptance, vertical validation, hardware/acoustic proof or project
completion; the C79 provider-audio failure and all nine broad project gates
remain open.

## Current hermetic rejection — 2026-09-13

PR #495 run `34754953782` tested candidate head
`ab0ada54292b9682879a98fc3b470e3b8c30df6a` in merge checkout
`5222672fe985c95f9f32128905fe687959887d0b`. Eight required lanes passed;
hermetic failed only the unchanged
`TestRunRoomWithResult_BidirectionalOverlapRecordsPeerOnlyEvidence` at
`s2s_room_realtime_replay_overlap_test.go:624` (`context deadline exceeded`)
and `TestRunSessionWithAudioOut_FinalizesPlayableWAV` at
`session_audio_out_test.go:79` (`WAV samples = 480 samples, want exact ordered
response`). The exact run/log hashes and full signatures are retained in
`ci-rejection-34754953782.json`.

Neither failure is in the C109 diff against `origin/main`; they are routed to
the existing room/liveness and audio-output owners. C109 did not edit, waive,
relabel or claim either failure repaired, and does not retry the unchanged
candidate. Fresh owned evidence remains green: analyzer 4/4, verifier 8 checks
plus 19 negatives, positive public 18/18 in 253.568s, and malformed/canceled
3/3 in 29.471s. No CI-green, review, merge, C61/C83 acceptance, vertical,
hardware/acoustic or project-completion claim is made.
