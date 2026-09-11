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
