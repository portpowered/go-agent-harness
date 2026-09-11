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

## Submitted head

Evidence-only checkpoint `c86fb9a6687dfcb38e35447b42242ca64d6742db` is pushed to `codex/audio-runtime-c47-live-probe-output-bound` and contains the repair ledger, focused reports and exact raw staged run. No executable Python source, Go source, immutable artifact, fixture or peer path changed after tested source `7b03664b9c355709c592998026b093ab547f6290`.

PR #439 is open at this head against `main` and is ready for the script-owned CI gate. No CI result, independent review, guarded merge, vertical acceptance or project acceptance is claimed here.
