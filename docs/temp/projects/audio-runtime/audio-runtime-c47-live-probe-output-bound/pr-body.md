## Summary

- Add live bounded stdout/stderr capture with one shared output budget across the staged controls.
- Terminate and reap complete process groups with bounded diagnostics while preserving the first causal failure.
- Enforce aggregate deadlines and measured storage reserve throughout staging and execution.
- Preserve primary parent exit failures, make cleanup inspection failure-safe, whitelist C47-owned output roots, and record timestamped reserve samples plus final post-outcome report size.
- Stage binaries using APFS clone-or-independent-copy semantics, preserve artifact immutability, and keep all candidate evidence task-local.
- Preserve and run the C45 focused controls plus new C47 causal regressions.

## Verification

- `test_resource_bounds.py --group capture --child-timeout 10 --total-timeout 120`
- `test_resource_bounds.py --group staging --child-timeout 10 --total-timeout 120`
- `test_resource_bounds.py --group all --child-timeout 10 --total-timeout 120`
- `run_c45_controls_private.py --child-timeout 60 --total-timeout 300`
- Clean-head repair checkpoint: `98ec09a4f0e27864e3d8e21967b8a70c73caed5f`, which preserves the first primary failure when cleanup inspection raises and adds a causal regression for that ordering.
- Exact staged mission against the admitted C44 artifact bundle, with a 2 GiB reserve and 64 MiB aggregate output cap: `evidence/staged-probe-live-07/latest-staged-probe.json` (`tested_source_revision=98ec09a4f0e27864e3d8e21967b8a70c73caed5f`, `decision=ACCEPTED`).
- The final exact report records `report_bytes=379480`, scratch cleanup `57472980 -> 0` with no errors, artifact equivalence, timestamped reserve samples, and the C47-owned reserve filesystem root. A prior live-06 miss was a single nondeterministic interruption-boundary result and is retained as diagnostic evidence, not acceptance evidence.

The exact staged mission report and raw control diagnostics are under the task-local `evidence/` directory. CI status is intentionally not claimed here; the open candidate is handed to the script CI gate.

## Current review repair handoff

This current head supersedes the earlier live-07 provenance that the concluded review rejected. Commit `e0c51bb40cd41a9f82fa6e76e2fb46118954be3c` contains the post-Popen whole-group cleanup repair and regression, replay/metadata/report reserve projection, explicit original/integrated/current provenance, the PRD-named C45 regression and Python compile drivers, and normalized live-04/live-07 stderr metadata.

The committed-head exact probe is `runs/latest-staged-probe.json` with raw controls under `runs/runs/staged-probe-20260911T042942Z-47589/`. It is `ACCEPTED` with `tested_source_revision=e0c51bb40cd41a9f82fa6e76e2fb46118954be3c`, original source `9f869d1db0a1724128f7c7d083a0054270def68`, integrated C44 source `5f14c45313cfdc71e000fda209e3408fcf863faf`, verifier SHA256 `7e79e235865826942b002fa50e10031892ec287e3ee6cb4bb14c5aff1871c6c3`, projected growth `174966628`, minimum observed free space `148249047040`, report bytes `380412`, latest report bytes `65819`, and scratch cleanup `57472980 -> 0` with no errors. Artifact input equivalence is true; APFS clone staging was used; forbidden verifier helpers were not called.

Focused gates are green for this candidate: C47 capture/combined resource controls, private unchanged C45 regressions with original revision `5f14c45313cfdc71e000fda209e3408fcf863faf`, all changed Python source compilation, and `git diff --check`. This handoff is ready for the script CI gate; no CI result is claimed or polled here.

## Final-head refresh

The exact staged mission was rerun once on candidate head `c8853168fb61281333e40cbb1def4e66771a550e` with the isolated C47 worktree output root. It returned `decision=ACCEPTED`; raw controls are under `runs/runs/staged-probe-20260911T045122Z-59482/`. The report records original source `9f869d1db0a1724128f7c7d083a0054270def68`, integrated C44 source `5f14c45313cfdc71e000fda209e3408fcf863faf`, current tested source `c8853168fb61281333e40cbb1def4e66771a550e`, verifier SHA256 `7e79e235865826942b002fa50e10031892ec287e3ee6cb4bb14c5aff1871c6c3`, staged-probe SHA256 `7170b42d3fb54f032565614d85e66c213df3ea79eb993cd08af80fa8e86f66db`, immutable input equivalence, projected growth `174966628`, minimum free space `146395197440`, `report_bytes=380939`, and scratch cleanup `57472980 -> 0` with no errors.

The combined C47 resource suite, private unchanged C45 regressions pinned to `5f14c45313cfdc71e000fda209e3408fcf863faf`, Python source check, and diff check are green. The prior script-CI rejection was inspected in full: the only failed check was the unrelated remote-device high-rate Go integration trial (`TestAgentBinaryTest45HighRateToolAudioRegression/trial_02`) with a remote snapshot deadline and missing final marker; C47 does not own that runtime path. This PR body reports executor evidence only; CI, review, merge and post-merge acceptance remain external.

## Current-main integration and handoff

- Fetched `origin/main=bb29005d0bb545db5e08ffda0205929b021d4fc1` and merged it in this isolated worktree as `f4e45506f236823198c2ed922cfabaef86830b3e`. The required baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, and current `origin/main` are ancestors of the candidate; the running host checkout and peer paths were not reset or edited.
- Review-13 repairs remain present: post-Popen whole-group TERM/KILL/reap with bounded fail-closed inspection and a reader-start descendant regression; full replay/metadata/report reserve projection; explicit original/integrated/current source provenance; the PRD-named regression and Python-source drivers; and normalized invalid-self-play stderr evidence.
- The post-merge exact staged mission is `evidence/staged-probe-live-08/latest-staged-probe.json`, with raw controls under `evidence/staged-probe-live-08/runs/staged-probe-20260911T070747Z-72217/`. It returned `decision=ACCEPTED` at tested source `f4e45506f236823198c2ed922cfabaef86830b3e`, with original source `9f869d1db0a1724128f7c7d083a0054270def68`, integrated C44 source `5f14c45313cfdc71e000fda209e3408fcf863faf`, verifier SHA256 `7e79e235865826942b002fa50e10031892ec287e3ee6cb4bb14c5aff1871c6c3`, and staged-probe SHA256 `7170b42d3fb54f032565614d85e66c213df3ea79eb993cd08af80fa8e86f66db`.
- The immutable YUI/consumer/source-snapshot/descriptor hashes remain `d8820356f3d1020875c013553aa5614af44f319b8c2b701a36f0e7c6882a8efe`, `5d18828a28f7169c06b280ae9b023335126bd8252c9296d8802cab8b2137449b`, `9a803dab9a439211ecf90617c5f063d8f3e27a4e1f8950d7006bd729170ff399`, and `e62c5da67ff259dfdfe5ade71f2fb2d654b12617b58e84767d853c95a2319d11`; `artifact_input_equivalence=true`. No executable was rebuilt. The run used APFS clones, projected `174966628` bytes, observed minimum free space `137280724992`, produced `4768` bytes of binary output, and cleaned scratch `57472980 -> 0` with no errors; final report bytes are `383494` and latest-report bytes `67622`.
- After integration, `test_resource_bounds.py --group all --child-timeout 10 --total-timeout 120`, `run_c45_regressions.py --original-revision 5f14c45313cfdc71e000fda209e3408fcf863faf --child-timeout 60 --total-timeout 300`, `check_python_sources.py`, and `git diff --check` all pass. The bounded suite retains the intentional measured shortfall as `BLOCKED` without launching.
- The latest current-head CI rejection was read in full from run `34563957146`, job `103152135348`, at prior head `c5aeb8103de499a32c4eca78fde308329fa4e65e`: every required check passed except the separate C38-owned `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`, which timed out at `36.99s` with `final_marker=false`, `rendered_pcm=462240`, `expected_pcm=174391`, seven expected tool calls/results, no queue/drop/overflow/discard loss, and the child still running. No C47-owned source is implicated and no out-of-lease repair was made.
- This is executor evidence only: CI, independent review, guarded merge, post-merge vertical acceptance, and project acceptance remain external. The changed integrated head is ready for the script CI gate; do not poll it here.

## Final pushed-head checkpoint

Candidate commit `cc0841b433ca3a00029cf6d0e89b435e713bfb93` records and pushes the current-main merge and exact live-08 evidence on `codex/audio-runtime-c47-live-probe-output-bound`; this final PR-body checkpoint is the remaining handoff metadata. Admission remains `admitted`, the worktree is clean, and PR #439 is open against `main` for the final pushed handoff head. The next action is the script CI gate; executor does not poll or claim CI, independent review, merge, vertical acceptance or project acceptance.

## Current-main integration and exact-head resubmission

- Fetched and merged `origin/main=cdf416a8060251a6b4bbf3680241f1ee79953cff` into the preserved branch without conflicts as `eef060a368706f01eb8f642287831e3060e547e8`. Startup `8bdafc7f947a3a2c9856220abdc539437035bd21`, required baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and current `origin/main` are ancestors. No host checkout, peer worktree, historical artifact, source, baseline, or predecessor checkpoint was reset or edited.
- Focused post-merge checks pass: `test_resource_bounds.py --group all --child-timeout 10 --total-timeout 120` (`ACCEPTED`, `7.225459s`), private `run_c45_regressions.py --original-revision 5f14c45313cfdc71e000fda209e3408fcf863faf --child-timeout 60 --total-timeout 300` (`ACCEPTED`), `check_python_sources.py` (`ACCEPTED`, seven sources), and `git diff --check`. The resource suite retains its deterministic reserve-shortfall `BLOCKED` negative (`available=2197842496`, `needed=2197842497`) before staging/launch.
- The exact staged mission rerun used the immutable C45 staged root read-only and the isolated C47 output root, with no executable rebuild. `runs/latest-staged-probe.json` is `decision=ACCEPTED` at tested source `eef060a368706f01eb8f642287831e3060e547e8`; raw controls are under `runs/runs/staged-probe-20260911T074409Z-51073/`. Immutable YUI/consumer/source-snapshot/descriptor hashes remain the PRD values and `artifact_input_equivalence=true`. Verifier SHA-256 is `7e79e235865826942b002fa50e10031892ec287e3ee6cb4bb14c5aff1871c6c3`; staged-probe SHA-256 is `7170b42d3fb54f032565614d85e66c213df3ea79eb993cd08af80fa8e86f66db`.
- The run projected `174966628` bytes, used APFS clone staging, observed minimum free space `134348144640`, `binary_output_bytes=4718`, `report_bytes=380685`, `latest_report_bytes=66071`, and cleaned scratch `57472980 -> 0` with no errors. `forbidden_helpers_called=[]`; no process survivors were reported. This is executor evidence only; script CI, independent review, guarded merge, post-merge vertical acceptance and project acceptance are not claimed.
- Exact next action: submit this changed same-task head to the script CI gate. CI is not polled here; actionable exact rejections remain with this executor under `CONTINUE`.

## Current-main integration and exact-head resubmission

- Fetched and merged `origin/main=6b4f32b5940d43192774e86f05e3c7c9293c71e3` cleanly as local merge `7da52f13bfaab7f2a2401d08eddc719ea3001afe`; current main is an ancestor. Existing C47 repairs, startup/baseline ancestry, historical artifacts and predecessor checkpoints were preserved.
- Focused checks on the merged tree pass: C47 resource bounds (including the intentional preflight `BLOCKED` storage negative), private C45 regressions pinned to `5f14c45313cfdc71e000fda209e3408fcf863faf`, seven-source Python compilation, and `git diff --check`.
- Exact staged mission: `runs/latest-staged-probe.json`, raw controls `runs/runs/staged-probe-20260911T082513Z-48305/`, `decision=ACCEPTED`, tested source `7da52f13bfaab7f2a2401d08eddc719ea3001afe`. Original source `9f869d1db0a1724128f7c7d083a0054270def68`, integrated C44 source `5f14c45313cfdc71e000fda209e3408fcf863faf`, verifier SHA256 `7e79e235865826942b002fa50e10031892ec287e3ee6cb4bb14c5aff1871c6c3`, staged-probe SHA256 `7170b42d3fb54f032565614d85e66c213df3ea79eb993cd08af80fa8e86f66db`.
- The immutable YUI/consumer/source-snapshot/descriptor hashes remain the PRD values with `artifact_input_equivalence=true`; APFS clone staging, projected growth `174966628`, minimum free space `130562568192`, `binary_output_bytes=4718`, `report_bytes=380955`, latest report `66340`, and scratch cleanup `57472980 -> 0` with no errors were observed. No executable was rebuilt and no forbidden helper was called.

Executor evidence only: script CI, independent review, guarded merge, post-merge vertical acceptance and project acceptance remain external. This changed same-task head is ready for the script CI gate; CI is not polled here.

## Refresh helper repair

The latest canonical C47 rejection identified an owned evidence-refresh defect: arbitrary paths could be written, symlink escapes were not fail-closed, and `latest_report_bytes` could remain stale after the run report changed.

This candidate adds canonical C47 `runs/<run>` path validation, rejects foreign and symlink-backed run/report targets before mutation, validates JSON objects, and stabilizes both `report_bytes` and `latest_report_bytes` in the run and latest reports. The C47 resource suite adds positive convergence and negative foreign-path, escaping-symlink, symlinked-latest-report, and historical-sentinel controls.

Focused repair evidence on this candidate:

- `test_resource_bounds.py --group all --child-timeout 10 --total-timeout 120`: `ACCEPTED` in `7.271088s`; deterministic reserve shortfall remains `BLOCKED` before launch.
- `run_c45_regressions.py --original-revision 5f14c45313cfdc71e000fda209e3408fcf863faf --child-timeout 60 --total-timeout 300`: `ACCEPTED`, including the original negative, aggregate-output and deadline controls.
- `check_python_sources.py`: `ACCEPTED` for all seven sources; `git diff --check`: pass.

The final committed-head exact staged mission is `runs/latest-staged-probe.json` with raw controls under `runs/runs/staged-probe-20260911T092202Z-97943/`. It returned `decision=ACCEPTED` at tested source `7b03664b9c355709c592998026b093ab547f6290`, with original source `9f869d1db0a1724128f7c7d083a0054270def68`, integrated C44 source `5f14c45313cfdc71e000fda209e3408fcf863faf`, verifier SHA256 `7e79e235865826942b002fa50e10031892ec287e3ee6cb4bb14c5aff1871c6c3`, and staged-probe SHA256 `7170b42d3fb54f032565614d85e66c213df3ea79eb993cd08af80fa8e86f66db`.

Immutable YUI, consumer, source snapshot and descriptor hashes remain the PRD values with `artifact_input_equivalence=true`; APFS clone staging was used. The run projected `174966628` bytes, observed minimum free space `125790531584`, produced `4718` bytes of binary output, and recorded `report_bytes=381193` equal to the run-tree measurement and `latest_report_bytes=66599` equal to the latest-report file. Scratch cleanup was `57472980 -> 0` with no errors, no survivors and no forbidden helpers. Post-commit C47 resource controls, private C45 regressions, seven-source compilation and diff check pass; the deterministic low-space control remains `BLOCKED` before launch.

This remains executor evidence only; CI, independent review, guarded merge, vertical acceptance and project acceptance are external.

## Current-main resubmission — 2026-09-11

The previously blocked C53 runtime prerequisite is now present on current `origin/main=ab5fbade1bd69712deb8fcd84e605c0ce9916112`; its accepted merge `9779b2364cd00516b9b227f5b3f10275bfe43c7a` is an ancestor. This isolated branch integrated that main as merge commit `3eed77cf437fa4f0eb3fe647d46132854b5a6a0e`, with the required startup and baseline ancestry preserved and no host checkout or peer path changes.

The immutable staged mission was rerun at tested source `3eed77cf437fa4f0eb3fe647d46132854b5a6a0e` with raw controls under `runs/runs/staged-probe-20260911T122526Z-5881/`. It returned `decision=ACCEPTED`; original source is `9f869d1db0a1724128f7c7d083a0054270def68`, integrated C44 source is `5f14c45313cfdc71e000fda209e3408fcf863faf`, verifier SHA256 is `7e79e235865826942b002fa50e10031892ec287e3ee6cb4bb14c5aff1871c6c3`, and staged-probe SHA256 is `7170b42d3fb54f032565614d85e66c213df3ea79eb993cd08af80fa8e86f66db`. Artifact input equivalence is true, APFS clone staging was used, projected growth is `174966628`, minimum observed free space is `105801224192`, binary output is `4716` bytes, `report_bytes=380270`, `latest_report_bytes=65745`, and scratch cleanup is `57472980 -> 0` with no errors, survivors or forbidden helpers.

The post-merge C47 resource controls, private C45 regressions pinned to the original revision, seven-source Python compilation and `git diff --check` all pass. The deterministic low-space control remains intentionally `BLOCKED` before launch. The PRD-literal host-checkout output root was rejected before staging by the C47 isolation guard; the equivalent mission used the isolated C47-owned output root while retaining the immutable staged input.

This candidate is ready for the script-owned CI gate. No CI result, independent review, guarded merge, post-merge vertical acceptance or project acceptance is claimed. Any CI rejection is to be inspected and repaired through this same task.

## Submitted head

Evidence-only checkpoint `40c011bc2f09d2f4fe128e50ebfb30ec2b3a0091` is pushed to `codex/audio-runtime-c47-live-probe-output-bound`; PR #439 is open at this head against `main`. The candidate is handed to the script-owned CI gate. No CI result, independent review, guarded merge, post-merge vertical acceptance or project acceptance is claimed.

## Latest main refresh

`origin/main=904e1f4c3be6c1e629138632573bd2fb55d50938` was fetched and merged cleanly as candidate merge commit `5e26dde970502d8b559e34f1a81075a297dcbcff`; the C53 accepted merge remains in its ancestry. The exact immutable staged mission then returned `decision=ACCEPTED` at raw run `runs/runs/staged-probe-20260911T123218Z-10131`, with original source `9f869d1db0a1724128f7c7d083a0054270def68`, integrated C44 source `5f14c45313cfdc71e000fda209e3408fcf863faf`, verifier SHA256 `7e79e235865826942b002fa50e10031892ec287e3ee6cb4bb14c5aff1871c6c3`, and staged-probe SHA256 `7170b42d3fb54f032565614d85e66c213df3ea79eb993cd08af80fa8e86f66db`.

Artifact input equivalence is true, APFS clones were used, projected growth is `174966628` bytes, minimum observed free space is `109386801152`, binary output is `4718` bytes, `report_bytes=381458`, `latest_report_bytes=66860`, and scratch cleanup is `57472980 -> 0` with no errors, survivors or forbidden helpers. Post-merge resource bounds, pinned C45 regressions, seven-source compilation and `git diff --check` pass; the reserve-shortfall control remains intentionally blocked before launch.

The latest-main evidence is ready for the script-owned CI gate. No CI result, independent review, guarded merge, post-merge vertical acceptance or project acceptance is claimed.

## Current-main WebMCP repair handoff

The current-main CI rejection on PR #439 identified a bounded WebMCP defect in `productionTargetProbe.Probe`: a persisted listed selection in `browserID/targetID` form was compared verbatim with the raw target ID, so the selected target was treated as unknown. Source checkpoint `c1ab430a8b76af3f06f88b9194f2377246a6d457` fixes that within the amended lease by splitting the composite reference, requiring the live browser ID to match, and then comparing the normalized raw target ID. The extra probe test was consolidated into `webmcp_composite_ref_test.go` and removed, preserving the 147-file CLI package baseline; bare and composite success plus unrelated restored-target fail-closed controls are covered.

Focused evidence on the committed source is green: the exact composite test passed five repetitions; the adjacent WebMCP selection/probe set and full `agent-cli/internal/transport/cli` package passed; `make architecture-size-check` passed at 185 packages/1891 files/27914 functions; and `git diff --check` passed. The accumulated C47 resource suite returned `ACCEPTED` in 7.2438s with its measured reserve-shortfall negative intentionally `BLOCKED` before launch (`available=2197842496`, `needed=2197842497`). Private C45 regressions pinned to `5f14c45313cfdc71e000fda209e3408fcf863faf` returned `ACCEPTED` in 1.63785s, and all seven Python sources compiled successfully.

The exact immutable staged mission used the read-only C45 artifact root and isolated C47 output root. `runs/latest-staged-probe.json` is `ACCEPTED` at tested source `c1ab430a8b76af3f06f88b9194f2377246a6d457`; raw controls are under `runs/runs/staged-probe-20260911T131838Z-38590/`. Artifact input equivalence is true, APFS clone staging was used, projected growth is 174966628 bytes, minimum observed free space is 103742377984 bytes, binary output is 4718 bytes, `report_bytes=380416`, `latest_report_bytes=65811`, and scratch cleanup is `57472980 -> 0` with no errors or survivors.

The changed source and C47-owned evidence are ready to push on the same branch and hand to the script CI gate. This is executor evidence only: no CI result, independent review, guarded merge or vertical/project acceptance is claimed or polled.

## Submitted head

Evidence-only checkpoint `c86fb9a6687dfcb38e35447b42242ca64d6742db` is pushed to `codex/audio-runtime-c47-live-probe-output-bound` and contains the repair ledger, focused reports and exact raw staged run. No executable Python source, Go source, immutable artifact, fixture or peer path changed after tested source `7b03664b9c355709c592998026b093ab547f6290`.

PR #439 is open at this head against `main` and is ready for the script-owned CI gate. No CI result, independent review, guarded merge, vertical acceptance or project acceptance is claimed here.

## Latest script-CI rejection and next dependency

The latest exact rejection is run `34584105269`, job `103214186726`, at head `5efc6a1e0f5e7d1c65debf562b53758b8b931125`. Every other required check passed. The only failure is the separate C53-owned production-binary remote-device control `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst` (`session_tool_audio_remote_e2e_test.go:183`): `41.88s`, `rendered_pcm=463200`, `expected_pcm=174391`, `final_marker=false`, seven tool calls/results, no queue/drop/overflow/discard loss, and the child still running. The full log is preserved at `/tmp/audio-runtime-c47-ci-integration-34584105269.log`.

C47 has no authorized source path in this failure; its Python/evidence candidate remains locally verified. The active C53 runtime repair must reach reviewed main first. After that merge, C47 will integrate the new main, rerun the immutable staged probe and focused controls, and submit the same PR through script CI. No CI green, review, merge, vertical acceptance or project acceptance is claimed.

## Final current-head handoff — supersedes the earlier dependency note

C53's accepted prerequisite is present in `origin/main=904e1f4c3be6c1e629138632573bd2fb55d50938`, which is an ancestor of the current candidate. The current C47 source repair is `c1ab430a8b76af3f06f88b9194f2377246a6d457`; evidence checkpoint `d2a1cba345f7afe0f41d74dd709b06c60ac7999b` is pushed, PR #439 is open at that head, the worktree is clean, and the current main ancestry is preserved.

The exact composite WebMCP test passed five repetitions, the adjacent/full CLI package checks passed, architecture-size-check passed at 185/1891/27914, the bounded C47 resource suite and private C45 regressions are `ACCEPTED`, all seven Python sources compile, and the exact immutable staged mission is `ACCEPTED` at `c1ab430` with raw run `runs/runs/staged-probe-20260911T131838Z-38590/`. The prior CI defect was the composite-selection capability mismatch addressed by this source checkpoint; no new CI result is being claimed or polled.

The next owner is the script CI gate on PR #439 head `d2a1cba345f7afe0f41d74dd709b06c60ac7999b`. Independent review, guarded merge and post-merge vertical/project acceptance remain external; any exact new gate rejection returns to this same task.

## Exact-head executor revalidation — 2026-09-11

The same clean candidate was revalidated at `49d809eac72688d3c5e8bf0f3934bb1632341005` after fetching `origin/main=904e1f4c3be6c1e629138632573bd2fb55d50938`; main remains an ancestor. The exact composite-selection regression passed five repetitions, the adjacent WebMCP selection/probe set and full CLI transport package passed, and `make architecture-size-check` passed at 185 packages/1,891 files/27,914 functions. The resource suite returned `ACCEPTED` in 7.834137s with its intentional reserve-shortfall negative `BLOCKED` before launch; private C45 regressions pinned to original revision `5f14c45313cfdc71e000fda209e3408fcf863faf` returned `ACCEPTED` in 2.32164s; all seven Python sources compiled; and `git diff --check` passed.

This refresh changed only C47-owned evidence reports. No executable was rebuilt, and the existing exact immutable staged mission remains pinned to source repair `c1ab430a8b76af3f06f88b9194f2377246a6d457` with equivalent inputs. This is executor evidence only: the same PR is ready for script CI handoff; CI, independent review, guarded merge and vertical/project acceptance are not claimed or polled.

## Exact-head C47/C54/C55 composition — 2026-09-11

- Following the current implementation handoff, this branch preserves C47 `8df99466542ce6a3006471290db101e4657680ef`, integrates C54 `64368377599e9eb4915400cd330eed59ca360fe1` as merge `7a40ef2fa724ef1204b7841e85d09a2f35b2b295`, and integrates C55 `dfc2336c64bbf8c657ea9925cb9d6e6e2d0ba24e` as merge `2047cbfb7e880b5d58f74b856da833da9aec6916`. The required `origin/main=904e1f4c3be6c1e629138632573bd2fb55d50938` ancestry is preserved; C47 direct transport remains exactly 147 Go files.
- The C54 room baseline was preserved downward-only: the seven stale `agent-cli/internal/room/mesh.go` entries exposed by composition were removed, while the existing `manifest_test.go` reduction from 618 to 607 lines remains. `make architecture-size-check` passes at 186 packages, 1,896 files and 27,963 functions; `make wire-check` regenerated the rooms graph with no diff.
- The focused composite WebMCP/managed-browser transport set passes normally and under `-race` for five repetitions, including the previously flaky managed-browser cleanup case; the full `agent-cli/internal/transport/cli` package passes in 16.376s. The accumulated rooms normal and race suites pass. C47 resource bounds return overall `ACCEPTED` with the deterministic low-space case truthfully `BLOCKED` before launch; private C45 regressions pinned to original revision `5f14c45313cfdc71e000fda209e3408fcf863faf` return `ACCEPTED`; all seven Python sources compile; and `git diff --check` passes.
- The immutable staged mission uses the read-only C45 artifact root and isolated C47 `runs/` output root. Raw controls are under `runs/runs/staged-probe-20260911T164751Z-75723/`; `runs/latest-staged-probe.json` is `ACCEPTED` at tested source `43ae91f58acefb14a214c1725c5a3c08ca6c8339`. Original source is `9f869d1db0a1724128f7c7d083a0054270def68`, integrated C44 source is `5f14c45313cfdc71e000fda209e3408fcf863faf`, verifier SHA256 is `7e79e235865826942b002fa50e10031892ec287e3ee6cb4bb14c5aff1871c6c3`, and staged-probe SHA256 is `7170b42d3fb54f032565614d85e66c213df3ea79eb993cd08af80fa8e86f66db`.
- The four immutable artifact inputs remain hash-equivalent before/after (`d8820356f3d1020875c013553aa5614af44f319b8c2b701a36f0e7c6882a8efe`, `5d18828a28f7169c06b280ae9b023335126bd8252c9296d8802cab8b2137449b`, `9a803dab9a439211ecf90617c5f063d8f3e27a4e1f8950d7006bd729170ff399`, `e62c5da67ff259dfdfe5ade71f2fb2d654b12617b58e84767d853c95a2319d11`); APFS clone staging was used. Projected growth is `174966628` bytes, minimum observed free space is `90123264000`, binary output is `4718` bytes, `report_bytes=380523`, `latest_report_bytes=65938`, and scratch cleanup is `57472980 -> 0` with no errors, survivors or forbidden helpers.

This is executor evidence only. The combined candidate is ready to push to PR #439 and hand to the script-owned CI gate; no CI result, independent review, guarded merge, post-merge vertical acceptance or project acceptance is claimed or polled.

## Combined-head CI rejection diagnosis — 2026-09-11

The current combined head `e0d4ba241b8018ae77857969cad08b6d578abd8f` was rejected by CI run `34624302582` in two independent checks. Coverage timed out on the C55-owned room-liveness projection `TestRoomUsesLiveTimeoutForPeerFilteredEventsAndEvidence`. Integration failed the C47-assigned `TestAgentBinaryTest46HighRateToolAudioRegression/trial_06` at `session_tool_audio_remote_e2e_test.go:219`, rendering `167991/174391` compared samples and losing exactly `6400` samples. The provider observed all nine responses and seven tool results; the remote device reported `Dropped=0`, `Overflow=0`, `Discarded=0`, and `DiscardEvents=0`, with `UnderflowEvents=65` and `UnderflowSamples=31200`.

The high-rate control was reproduced locally with the strict oracle unchanged: trial 06, the 20-trial test46 stress, eight repeated 20-trial stresses, the exact `make test-audio-device-server-integration` target, `GOMAXPROCS=2`, `GOMAXPROCS=1`, and `GOFLAGS=-parallel=20` all passed (the last exact target completed in `95.952s`). The deficit is exactly one 9600-sample provider delta at 24 kHz→16 kHz, but no local run identifies a runtime queue loss. This leaves CI host/process contention in the unconstrained 20-way fixture as a hypothesis, not a demonstrated product defect; C47 did not alter its strict counts, retention, timeouts, cleanup, C55 room paths or any unleased fixture/runtime path.

Evidence checkpoint `b8760165b422b8f97bab622b7f6bc0628dfe1c07` records the diagnosis and is pushed to the same branch. Do not resubmit `e0d4ba24` unchanged. Retain C47 until active C55 returns the exact room-liveness result and until either the high-rate loss reproduces or the primary grants the precise C47-owned path for a bounded causal repair; then rerun the formerly failing strict test plus focused normal/race and C47/C54/C55 regressions, integrate C55, and submit the changed same head to script CI without polling.
