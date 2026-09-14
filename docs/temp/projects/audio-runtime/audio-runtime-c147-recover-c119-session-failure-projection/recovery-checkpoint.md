# C147 recovery checkpoint

Recorded at 2026-09-13T20:27:57Z for the admitted `audio-runtime-c147-recover-c119-session-failure-projection` task.

## Admission and identity

- The required command was run from `FACTORY_ROOT`:
  `rtk python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c147-recover-c119-session-failure-projection`
- Exact result: `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c147-recover-c119-session-failure-projection"}`.
- The C147 setup worktree had branch `codex/audio-runtime-c147-recover-c119-session-failure-projection`, and its admitted `prd.json.branchName` matched that branch.
- C147 is the logical recovery identity. Delivery remains on the existing adopted C119 branch and worktree, as required by the C147 task record; no second project or acceptance waiver is used.

## Adopted candidate before repair

- Worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c119-retire-cli-session-failure-projection`
- Branch: `codex/audio-runtime-c119-retire-cli-session-failure-projection`
- Pull request: `#505`
- Candidate HEAD: `62cdc8657c3f515c66f311cfa577e45df9db8992`
- Candidate HEAD parents: `a15b8112c84b7f3a6eb98c176094237b29d97e11` and `4a1c399ccbb3d780be95eb04316e84b8f11a6646`
- Preserved remote head before recovery: `a15b8112c84b7f3a6eb98c176094237b29d97e11`
- Fetched current `origin/main`: `4a1c399ccbb3d780be95eb04316e84b8f11a6646`
- Required startup integration: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- Planning baseline: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`
- Both startup integration and current `origin/main` are ancestors of HEAD. Integration was already a normal merge; no reset, rebase, force-push, or host-checkout merge was performed.

## Preserved dirty checkpoint

Before any C147 repair, the adopted worktree had exactly these two dirty files:

- `agent-cli/internal/services/internal/agentruntime/session_failure_projection_adapter.go`
- `docs/temp/projects/audio-runtime/audio-runtime-c119-retire-cli-session-failure-projection/verify.py`

The exact binary diff SHA-256 was `70e97ec45c91e0be3ba0f5de70a54bf2d3481b2cd19cf87ce706192abdfae767`.

The complete pre-repair diff was:

```diff
diff --git a/agent-cli/internal/services/internal/agentruntime/session_failure_projection_adapter.go b/agent-cli/internal/services/internal/agentruntime/session_failure_projection_adapter.go
index bbd5a7377..220adebea 100644
--- a/agent-cli/internal/services/internal/agentruntime/session_failure_projection_adapter.go
+++ b/agent-cli/internal/services/internal/agentruntime/session_failure_projection_adapter.go
@@ -39,18 +39,6 @@ func (o *observer) captureFailureFromClose(v *m.SessionCloseValue) {
+	fi(o).Failure.AcceptClose(v, sfw.Progress(o.sawSessionOpen, o.turnsCompleted))
 }

-
-func p(o *observer, kind sf.Projection, e string) *failureFacts {
-	if o == nil {
-		return nil
-	}
-	return ff(sfw.Facts(kind, e, sfw.Progress(o.sawSessionOpen, o.turnsCompleted)))
-}
-func (o *observer) unresolvedToolResultFailureFacts(e string) *failureFacts { return p(o, u, e) }
-func (o *observer) imageContinuationFailureFacts(e string) *failureFacts    { return p(o, i, e) }
-func (o *observer) toolContinuationFailureFacts(e string) *failureFacts     { return p(o, t, e) }
-func (o *observer) scheduledAudioFailureFacts(e string) *failureFacts       { return p(o, a, e) }
 func (o *observer) emitToolCallRecord(v *m.ToolCallEndValue) {
+	if o != nil && o.sink != nil && v != nil {
+		fi(o).Failure.EmitUnsupportedTool(sf.ToolCall{Name: v.Name, ID: v.ToolCallID, TurnIndex: o.turnsCompleted + 1})
diff --git a/docs/temp/projects/audio-runtime/audio-runtime-c119-retire-cli-session-failure-projection/verify.py b/docs/temp/projects/audio-runtime/audio-runtime-c119-retire-cli-session-failure-projection/verify.py
index dc2e6539d..40b03c8df 100644
--- a/docs/temp/projects/audio-runtime/audio-runtime-c119-retire-cli-session-failure-projection/verify.py
+++ b/docs/temp/projects/audio-runtime/audio-runtime-c119-retire-cli-session-failure-projection/verify.py
@@ -170,6 +170,10 @@ def verify_scope() -> dict[str, Any]:
     require(not forbidden_changed, f"excluded caller/shared path changed: {forbidden_changed}")
     for path in FORBIDDEN:
         current = ROOT / path
+        current_main_has_path = bool(git("ls-tree", "-r", "--name-only", CURRENT_MAIN, "--", path))
+        if not current_main_has_path:
+            require(not current.exists(), f"excluded path was restored after current origin/main removed it: {path}")
+            continue
         accepted_main = git_bytes("show", f"{CURRENT_MAIN}:{path}")
         require(current.is_file() and current.read_bytes() == accepted_main, f"excluded fingerprint changed from current origin/main: {path}")
     require(not LEGACY.exists(), "legacy session failure source remains")
```

The C119 root `prd.json` and `progress.txt` were preserved byte-for-byte before mutation. Their recorded SHA-256 values are `9126fa29a974dfc23497245abc61472a6cb06f17f65946a04e05d2278223862f` and `4ce380ea1a579cdf16b6e3b6706db47daa31a83899d9228d37a00e066e89941d`, respectively.

## Prior findings retained

The prior independent-review rejection remains actionable and is not self-reviewed or waived:

> Independent service normal/race, hermetic consumer, and CLI focused normal/race probes passed; PR #505 run 34770679211 is 9/9 green. However, replay artifact run-1789315603-90141 and yui SHA 2f6634... are pinned to source a7459900, before current-main merge b0e8cebbc; handoff evidence still records stale main 915ed982. GitHub also reports mergeStateStatus DIRTY. Reconcile current main/PR state, rebuild yui, rerun all four bounded replay cases and affected verification, refresh exact head/binary evidence, push, and resubmit to Script CI without polling. Retain unwaived peer regression 34747817964 with 6400 lost samples.

The latest hermetic CI rejection `34768071404` was read in full and retained. Its exact failed subtest was the out-of-scope remote device/provider-burst test `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test48_matched_healthy_control/provider_burst`; no C119-owned production prefix changed and the exact subtest repeated successfully locally and post-merge. The same rejection records the peer C127-only missing coverage manifest `go-agent-runtime/services/session/internal/live/causal/package.json`; C119 does not claim that path. The unwaived peer regression `34747817964` records exactly 6,400 lost samples.

The predecessor CI evidence files remain preserved under the C119 namespace, including `ci-rejection-34761314555.json`, `ci-hermetic-rejection-34768071404.json`, `ci-coverage-rejection-34763656307.json`, and `ci-integration-rejection-34747817964.json`. The previous C119 admission evidence SHA-256 is `183781acee677d6f276bd81d4a326463fe5308e5d5ae72480aba33631674f92b`.

At this checkpoint no C119 predecessor PRD/ledger or host checkout was overwritten. The only new file is this C147 recovery record; the two inherited dirty changes above remain unchanged pending focused validation and exact current-head/artifact reconciliation.
