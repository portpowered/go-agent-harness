# C11 hermetic package assessment

## Decision

`fresh_timing=BLOCKED`. C08 owns the shared factory host and no isolated or
dedicated runner lease is available, so no fresh Go inventory, warm build, or
hermetic package timing was launched. `quiet-evidence-blocked.json` and the
before/after board, worker, process, and load snapshots are the immutable
fallback. This is not a performance PASS, waiver, or claim that the
under-three-minute target is met.

## Current candidate and admission

- Project/work: admitted `audio-runtime` / `audio-runtime-c11-hermetic-package-profile`.
- Session/server: `~default` / `http://127.0.0.1:7439`.
- Branch and `prd.json.branchName`: `codex/audio-runtime-c11-hermetic-package-profile`.
- Current implementation checkpoint: `ca12c8afea00777a62ed9490ea6b968b7d94d256`.
- Rebased `origin/main`: `02e54e6a89a7a2d7ad1ce2fa0619145c16400339`.
- Startup integration and baseline ancestors remain
  `8bdafc7f947a3a2c9856220abdc539437035bd21` and
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.

## Repairs and focused evidence

- Offline analysis now enforces the same quiet-evidence contract as heavy
  commands: current `captured_at_utc`/`valid_until_utc`, runner metadata,
  before/after load/lease observations, and captured summary provenance.
- The public controls cover cross-package concurrency, fail-closed heavy
  admission, containment, raw-artifact truth, source identity, repetition
  completeness, missing quiet metadata, and expired quiet evidence. The
  regenerated `ctrl/controls.json` reports 21 declared scenarios, 17 result
  groups, and zero Go/network/build invocations.
- Generated run group, repetition, and record names are bounded for Windows
  checkout portability. Superseded deep `controls-review-repair` artifacts
  were replaced by compact `ctrl` evidence; the longest new tracked relative
  path is 184 characters.
- `GOWORK=off go test . -count=1` in `tools/timingate`, Python AST parsing,
  `profile.py --help`, the 21-control suite, and `git diff --check` pass.

## CI rejection disposition

The latest current-head run `34227633235` was inspected in full. Eight checks
passed, but job `102065559982` (`CI (Windows audio portable)`) failed during
checkout with `Filename too long` before Go ran. The exact metadata and repair
are in `ci-rejection-windows.json`; the old rejected head was `5bdb5123`, and
the same PR is repaired at the checkpoint above. This candidate is not claimed
CI-green.

## Handoff

Push this same PR #403 and submit it to the script-owned CI gate. Do not poll
CI or self-review. Any exact current-head rejection returns to this task;
independent review, guarded merge, and post-integration vertical validation
remain external. All nine immutable project gates remain open.
