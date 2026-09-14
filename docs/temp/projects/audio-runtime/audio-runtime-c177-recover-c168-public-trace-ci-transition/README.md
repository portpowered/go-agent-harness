# C177 current-head CI handoff

Task: `audio-runtime-c177-recover-c168-public-trace-ci-transition`.

The admitted `audio-runtime` manifest was verified with
`project-control.py verify-work --type task --name audio-runtime-c177-recover-c168-public-trace-ci-transition`.
The logical C177 worktree branch is
`codex/audio-runtime-c177-recover-c168-public-trace-ci-transition`, matching
the C177 `prd.json`. The existing C117 public-trace worktree and PR #503 are
the single adopted candidate; no second project, waiver, or new PR was used.

## Candidate and failure classification

The adopted C117 candidate was `21734b0b2f4a619ff22d8527e18480e7ba865a4b`.
Current `origin/main` was fetched and integrated into the adopted worktree as
merge commit `e3fc5db1cc0fa91e00aa9157d2d35e8c6265466c`, with parents
`21734b0b2f4a619ff22d8527e18480e7ba865a4b` and
`8490f8dcad63adde99036016e1e7ffd9ecf61e34`.

The settled prior script run was `34791475655`: static, unit, integration,
race, hermetic, WebMCP Chrome, macOS audio release, and Windows audio
portable passed; only coverage failed. Its exact first failure was
`TestRunRoom_BoundGraceExpiryCancelsActiveResponseCleanly`, reporting
`session_room_bound_shutdown_test.go:242: active response cancellations = 0,
want exactly one`. That failure is in the C154 room-bound-grace owner paths,
not in C177's public-trace paths. The fetched current main contains the C154
repair from PR #520, so no C177-owned repair was justified.

## Focused evidence

All commands below were run in the adopted C117 worktree at the source head
above; the final handoff commit is documentation-only.

- Room-bound causal control: `TestRunRoom_BoundGraceExpiryCancelsActiveResponseCleanly` passed.
- Public trace normal and race tests passed.
- Shipped SIGINT integration tests for after-tool-result, during-tool-execution, and no-tool paths passed.
- `COUNT=1 bash scripts/test-session-ci-regressions.sh all` passed normal, coverage, and race modes across the CLI, integration, simulated device, and OpenAI provider lanes.
- `scripts/check-wire.py --go go agent-cli` passed and regenerated no tracked delta.
- `make architecture-check size-check` passed both gates.
- The bounded C146 replay matrix passed all six cases: simulated four-tap callback, render-tap-unavailable, malformed odd PCM, stale provenance, healthy audio/tool replay, and interruption continuation. The report was regenerated against the adopted source head and remained credential-free.
- `git diff --check` passed.

The legacy C108/C146 aggregate verifiers were also run for regression
visibility. Both stop at the same historical scope assertion:
`mutation outside owned C108 directory:
agent-cli/test/integration/s2s_sigint_clean_cancel_process_test.go` (the
C146 verifier phrases it as `candidate changed unowned paths`). That path is
explicitly listed in the admitted C177 `ownedPaths`; the older verifiers
predate C177 and were not modified. This is not an implementation failure or
a reason to alter a predecessor checkpoint.

## Handoff state

The candidate preserves the startup and predecessor ancestry, current main
ancestry, prior C117/C146/C168 evidence, and the C174 exclusion. The existing
PR #503 is the handoff surface. `ACCEPTED` means submit this candidate to the
script CI gate; it does not claim green CI, independent review, or guarded
merge.
