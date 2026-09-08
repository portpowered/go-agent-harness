# C11 canonical record and validation mapping

This checkpoint follows the committed C11 design decision. The manifest's
single `commands` array is the only command-record authority. Inventory,
warm-up, source-validation, full-lane, and cohort records all use the same
schema; repetition groups and source summaries are derived for display and
never carry copies of command records.

## Old representation to canonical boundary

| Previous representation or control | Canonical representation and retained behavior |
| --- | --- |
| Separate metadata/inventory/warm/run arrays | Replaced by one `manifest.commands` array. `validate_capture` validates every phase record, rejects legacy duplicate collections, and checks command identity, artifacts, timing, and required fields before aggregation. |
| `run_groups[].records` duplicated every command record | Removed from newly captured manifests. `derive_run_groups` and `validate_repetition_group` derive membership from canonical command records grouped by `run_group_id` and `repeat_index`. |
| `run_groups[].source_validations` duplicated per-record Git evidence | Removed from newly captured manifests. Source validation records live once in `commands`; run records retain only their two metadata record IDs. |
| `analyze --group` filtered records before validating the manifest | The complete canonical command stream and derived groups are validated first. The group option only filters ranking, lane-wall, and displayed repetition records; an invalid unselected group still makes analysis `INVALID`. |
| Repetition count and package scope controls | Derived from canonical records grouped by run-group id, module, and repeat index. Requested/completed counts, module coverage, selected package identity, quiet evidence, and package terminal coverage remain fail-closed. |
| Raw Go JSON and package timing | `parse_timing_stream` remains the single parser for retained stdout. Package/test failures, cache markers, malformed/truncated streams, no-test conflicts, overlap classification, and timing bounds remain observable. The existing `tools/timingate` 60-second policy is not reimplemented. |
| Source and metadata provenance | `validate_source_state` records Git head/status command artifacts once in `commands` per run and retains IDs in the run record. Analysis verifies artifact containment, bytes, hashes, exact argv/cwd/env Git identity, captured output, repository identity, expected head, and dirty paths before fresh timing can pass. |
| Quiet-runner evidence | Each group still carries its captured quiet-evidence reference. Analysis verifies the file hash, contained path, complete runner metadata, current validity window, before/after lease/process/load observations, isolation, and copied summary fields. |
| Failure/stale-output controls | Every malformed or incomplete input reaches the atomic `INVALID` writer; a prior `PASS` is replaced. Existing pass/fail/truncated/empty/cached/missing-package/no-test/overlap/timeout/artifact/schema/provenance/overflow controls remain. |

## Public causal control

`controls.py` now includes canonical phase mutations for malformed inventory and
warm records, a hermetic no-warm capture, and a malformed canonical command. It
also adds `unreferenced-invalid-run-group-filtered`, which appends a zero-request
canonical run record, asks the public analyzer to display only the valid group,
and asserts exit 1 and `status=INVALID`. Git command argv tampering is covered
through the source-identity control. These controls prove that phase validation,
schedule validation, and display filtering cannot hide a whole-input defect.

No failing assertion, timeout, timingate policy, runtime path, or acceptance
limit was removed or weakened. Historical manifests containing the removed
duplicate fields are retained as evidence; new captures use the canonical
shape and reject ambiguous duplicate group authorities.
