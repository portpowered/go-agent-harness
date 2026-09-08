# C11 canonical record and validation mapping

This checkpoint follows the committed C11 design decision. It keeps the
manifest's `runs` array as the only mutable command-record authority. A run
group remains a schedule/display description: group id, selected inventory,
requested/completed repetitions, quiet evidence, status, and failure summary.
It does not carry copies of command records or source-validation records.

## Old representation to canonical boundary

| Previous representation or control | Canonical representation and retained behavior |
| --- | --- |
| `run_groups[].records` duplicated every command record | Removed from newly captured groups. `validate_repetition_group` derives membership from `manifest.runs[].run_group_id` and `repeat_index`; duplicate copies are rejected instead of trusted. |
| `run_groups[].source_validations` duplicated per-record Git evidence | Removed from newly captured groups. Source validation stays on each canonical run record and is checked against its retained metadata command artifacts. |
| `analyze --group` filtered records before validating the manifest | All canonical records and all groups are parsed and validated first. The group option only filters ranking, lane-wall, and displayed repetition records; an invalid unselected group still makes the analysis `INVALID`. |
| Repetition count and package scope controls | Derived from canonical records grouped by run-group id, module, and repeat index. Requested/completed counts, module coverage, selected package identity, duplicate group ids, quiet evidence, and package terminal coverage remain fail-closed. |
| Raw Go JSON and package timing | `parse_timing_stream` remains the single parser for retained stdout. Package/test failures, cache markers, malformed/truncated streams, no-test conflicts, overlap classification, and timing bounds remain observable. The existing `tools/timingate` 60-second policy is not reimplemented. |
| Source and metadata provenance | `validate_source_state` records Git head/status command artifacts per run. Analysis verifies artifact containment, bytes, hashes, exact captured head/status output, repository identity, expected head, and dirty paths before fresh timing can pass. |
| Quiet-runner evidence | Each group still carries its captured quiet-evidence reference. Analysis verifies the file hash, contained path, complete runner metadata, current validity window, before/after lease/process/load observations, isolation, and copied summary fields. |
| Failure/stale-output controls | Every malformed or incomplete input reaches the atomic `INVALID` writer; a prior `PASS` is replaced. Existing pass/fail/truncated/empty/cached/missing-package/no-test/overlap/timeout/artifact/schema/provenance/overflow controls remain. |

## Public causal control

`controls.py` now includes `unreferenced-invalid-run-group-filtered`: it adds a
zero-request invalid group, asks the public analyzer to display only the valid
group, and asserts exit 1, `status=INVALID`, and a retained failure reference
for the unselected invalid group. This proves display filtering cannot hide a
whole-input validation failure.

No failing assertion, timeout, timingate policy, runtime path, or acceptance
limit was removed or weakened. Historical manifests containing the removed
duplicate fields are retained as evidence; new captures use the canonical
shape and reject ambiguous duplicate group authorities.
