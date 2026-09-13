# C115 repaired decoder termination checkpoint

## Identity and preservation

- Work `audio-runtime-c115-centralize-filesystem-audio-codec` remains admitted
  to the sole `audio-runtime/audio-runtime-v1` project. The task verification
  returned `status: admitted`; the isolated branch matches `prd.json` exactly.
- The repaired source checkpoint is `459a5b74c5db68a1b5a884baa3aabd337c23e2d7`.
  It preserves startup `8bdafc7f947a3a2c9856220abdc539437035bd21`, accepted /
  planning main `3963bc3566da24f8214634c17a9d0f79a6724171`, and freshly fetched
  `origin/main=ea53be13ce5e4ef14fd8c89c695c21744a1f7686` ancestry.
- No host checkout, peer worktree, predecessor checkpoint, C79-owned shared
  registry or architecture baseline was reset or edited. PR `#501` remains
  the same open delivery identity.

## Review repair and focused validation

- Prior independent review findings are retained and repaired: composed tool,
  session and room runtime-service injection; returned temporary-file cleanup
  errors; immediate one-shot decoder cancellation/termination on output and
  stderr overflow; and the bounded shipped evidence workflow.
- The final owned repair records a non-nil decoder termination error and joins
  it with the primary output/stderr limit error. The deterministic regression
  proves `ErrOutputTooLarge`, `ErrProcessWait`, the original termination error,
  and exactly one termination attempt.
- Codec normal tests pass (`41` tests in `3` packages); codec race tests pass
  (`105` tests in `3` packages); focused filesystem race tests pass (`57` tests);
  the composed CLI tests pass (`160` tests in `2` packages); and the separate
  `GOWORK=off` consumer passes.
- Pinned lint and staticcheck report zero issues, vet passes, coverage
  registration checks `179` packages across `6` modules, `make test-regressions`
  passes the agent-cli and go-llm-gateway replay fixtures, and `git diff --check`
  passes.

## Exact shipped evidence

- The bounded run
  `runs/20260913T052551Z-73854/report.json` is `ACCEPTED` from source
  `459a5b7`; all five cases pass: shipped audio/text tool use,
  malformed/truncated rejection, fake decoder overflow and cleanup identity,
  C21 consumption replay plus external consumer, and C50 public replay.
- `verify.py --mode public-and-accumulated-regressions` passes at source
  `459a5b7`. The shipped artifact is `51,013,874` bytes with SHA-256
  `2454c63cddab4a3979beaa7658f1ad91cd2726def3a0437fbca21f51bc8a1122`;
  `artifacts/manifest.json`, `runs/latest.json`, and the verification summary
  are bound to that source revision. The run is credential-free and records
  clean shutdown; physical device and acoustic proof remain out of scope.

## Remaining external gate

- `make wire-check` passes. `make architecture-size-check` reports exactly two
  unchanged non-C115 baseline drifts: `agent-cli/internal/transport/cli`
  (`148 > 147`) and `internal/transport/cli/room.go` (`535 > 527`). No baseline
  was raised or changed; the shared C79 lease remains active.
- The latest script-CI rejection is recorded on prior HEAD `70955d6` as
  `CI (static)`; its settled C115-relevant architecture failure is the same
  shared baseline gate. The overall run was still in progress when inspected,
  so CI was not polled or relabeled green.

Next action: retain the same task and PR. After C79's reviewed guarded merge
and explicit release, fetch current `origin/main` without reset, reconcile the
same branch, apply only demonstrated C115 registry/baseline changes if still
needed, rerun the bounded scoped and accumulated regressions, push the changed
head, and submit that exact head to Script CI without polling. Fresh independent
review, guarded merge and the immutable engineering vertical probe remain
mandatory.
