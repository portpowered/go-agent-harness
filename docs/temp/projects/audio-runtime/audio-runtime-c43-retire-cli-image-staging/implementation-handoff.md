# C43 implementation handoff

Date: 2026-09-10

This is the Luna executor handoff for the sole admitted `audio-runtime` project
and task `audio-runtime-c43-retire-cli-image-staging`. It is not independent
review, CI completion, merge approval, vertical acceptance, or project
completion.

## Admission and ancestry

- `rtk proxy python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c43-retire-cli-image-staging` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c43-retire-cli-image-staging"}`.
- The isolated worktree is `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c43-retire-cli-image-staging` on `codex/audio-runtime-c43-retire-cli-image-staging`; the admitted task packet's `prd.branchName` and worktree match these values.
- The current-main integration checkpoint is merge `3dfd88d3`; fetched `origin/main` is `d6efc88d10e046396e777762802f5f731c9c6d9d`. Required baseline/startup ancestry remains preserved. The running host checkout and predecessor worktrees were not merged, reset, or edited.
- The implementation checkpoint tested below is commit `97b1141b10b13c19ef73750694fa4acda4cc1bb2`.

## Review-254 repair map

- The prior rejection required a shipped executable, a deterministic local provider, real Chrome WebMCP dynamic refresh, an induced-timeout cleanup proof, exact source/build/fixture provenance, and a 142-to-71-line source/symbol inventory. `verify.py` now builds and runs the public `yui` executable and a loopback-only WebSocket provider, drives a real Chrome WebMCP page, records complete command/output/exit observations, and archives source/build hashes.
- The process probe exposed a live migration defect: `go-agent-runtime/services/session/internal/live` received semantic `tools_added`/`tools_removed`/navigation events but its refresh predicate only recognized legacy catalog/generation names. The predicate now refreshes for those semantic events; `start_support_test.go` covers the positive event types and a non-refresh invocation event in normal and race tests.
- The local provider completes the protocol with a normal WebSocket close frame after `session.closed`, and treats the induced timeout's client close (including code 1006) as the expected cleanup outcome. It also rejects any authorization header other than the synthetic hermetic test key.

## Exact-head focused evidence

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c43-retire-cli-image-staging/verify.py --mode focused
```

Run: `runs/verify-20260910T200021Z-37030`; status `PASS`; 18 recorded steps;
tested source revision `97b1141b10b13c19ef73750694fa4acda4cc1bb2`.
The complete runner outcome, command arrays, stdout/stderr, exit codes, and
per-case observations are in that run's `outcome.json`,
`build-provenance.json`, and `process/*/observations.json` files.

Focused causal and accumulated checks all exited 0:

- public consumer build/test, positive exact-image proof, wrong-oracle negative control (expected exit 1 with `70` actual versus `71` expected), and post-cleanup negative control;
- live semantic capability refresh regression normal and `-race`;
- private image staging/Wire normal and race checks, CLI adapter normal and race checks, and all `go-agent-runtime/services/tools/...` regressions;
- in-process shipped CLI image regression;
- shipped process image workflow, induced timeout workflow, and credential-free strict tool replay;
- the process image workflow uses the literal 70-byte PNG, sends its exact staged absolute path to `read_image`, validates the typed `input_image` data URL, and observes the dynamic page tool `c43_refreshed_probe` in the second provider `session.update`.

Process provenance from `build-provenance.json`:

- `go env GOVERSION GOOS GOARCH`: `go1.26.7`, `darwin`, `arm64`;
- shipped CLI SHA-256: `6fa63456dac361c7bf9bdcf40030e54157e9f8e6cfda53ff4a1b83bf68526a6e`;
- local provider SHA-256: `7ac892d52f27bb1fcb426c6b83eb203f07bf5a1c3b08b1e5b1124e7961045c26`;
- provider source SHA-256: `f63381b1b55a10c095692938967a39a62bd77b6f50717c41278405aa97cd614a`;
- Chrome page source SHA-256: `703a8c098f75de45957f932a5cb979b5326befb24f8d21f66d6645db6918f698`;
- literal fixture: 70 bytes, SHA-256 `4ff6ab670a58c14270e034e2090d9a432caa263a14e0a25785386b0c12f880b5`;
- strict replay capture SHA-256: `b6541a87a39e065151f45553ff5b9e788e2d9d57576a15c285eeac071a1476ea`, config SHA-256: `84a450eec67059fa1d0a81ad80651ff4f04fdcee0f7585b514774c00f16e1d07`.

Image process observation: CLI exit `0`; provider status `PASS`; page events
`initial, refreshed`; initial catalog contains `c43_initial_probe`, refreshed
catalog contains both `c43_initial_probe` and `c43_refreshed_probe`; the
provider's validated image result and typed projection hash the fixture above;
staging leftovers are `[]`; CLI/provider/Chrome process-group survivors are
all `false`.

Induced-timeout observation: CLI exit `0`; provider status `PASS` with
`expected_timeout_client_close=true`; `--max-duration 2s`; staging leftovers
are `[]`; CLI/provider/Chrome process-group survivors are all `false`.

Credential-free replay observation: shipped CLI exit `0`, no timeout, no
process-group survivor, stdout contains `PROBE_TOOL_MARKER_9182` and
`strict replay continuation`, and the capture has no external provider or
credential dependency.

The required source delta is archived in the run's `source-delta.diff` and
`symbol-inventory.json`: baseline `926ded7bfa8f3c3e42115192d03aa1240c4806db`,
source `agent-cli/internal/services/internal/agentruntime/session_image_staging.go`,
142 baseline lines to 71 final adapter lines, source-delta SHA-256
`94652a9bec7dd6ee8241229e4d4aee24c57bcb9c44683d3c051981894403d6d4`.
Retired symbols are `sessionImageToolPathDescription`,
`sessionImageStageExtension`, and `advertiseSessionImagePaths`; retained
adapter symbols are `prepareSessionImageToolAccess` and
`sessionImageStagingConfigDir`.

## Handoff action and limits

Push commit `97b1141b10b13c19ef73750694fa4acda4cc1bb2` and its documentation
descendant to the existing PR `#434`, update the PR body with this evidence,
and submit the same task to the script-owned CI gate. Do not poll CI here and
do not claim green CI. If the script gate rejects a specific check, inspect
that exact log, repair this same task, and return `CONTINUE` while actionable
repairs remain.

This evidence is credential-free, loopback-only, and uses Chrome native
WebMCP. It does not prove physical microphone/speaker behavior, external
provider behavior, independent review, guarded merge, post-merge vertical
acceptance, or the immutable project-wide criteria.
