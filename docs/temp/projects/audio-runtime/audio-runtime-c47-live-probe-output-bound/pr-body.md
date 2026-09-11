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
- Exact staged mission against the admitted C44 artifact bundle, with a 2 GiB reserve and 64 MiB aggregate output cap: `evidence/staged-probe-live-04/latest-staged-probe.json` (`tested_source_revision=9974990cf94995e2584948d1074a8199d3778ae3`, `decision=ACCEPTED`).
- The repaired exact report records post-outcome `report_bytes=379244`, scratch cleanup `57472980 -> 0` with no errors, artifact equivalence, timestamped reserve samples, and the C47-owned reserve filesystem root.

The exact staged mission report and raw control diagnostics are under the task-local `evidence/` directory. CI status is intentionally not claimed here; the open candidate is handed to the script CI gate.
