# C118 session-terminal retirement evidence

`inventory.json` records the admitted project, immutable source baseline,
dual-ancestry checks, caller census, peer lease exclusions, and the current
task-board CI/review rejection and repair provenance. The current task is
`work-task-23`; accepted main `09c70f51` is merged by `5c423b65`, and the
current candidate is `5c423b65`. The independent review finding about
scheduled-incomplete nil/context failures was repaired in `4b4b10c1`.

`external-consumer` is a separate Go module. With `GOWORK=off`, its test imports
only the public `go-agent-runtime/services/sessionterminal` contract, its
generated Wire constructor, and the public provider taxonomy. It verifies
typed error identity, deterministic continuation metadata, accounting,
cancellation/output-state policy, and independent service construction.

Implementation checkpoints are `360d2a9`, `26365f5`, `55a652f`, the
documentation/evidence descendants `084de02`, `9a44039`, the coverage repair
`2ba46424`, the review repair `4b4b10c1`, the fresh-main merge `668d7b48`,
and the architecture-budget repair `9cf7a04`, followed by the fresh-main
merge `5c423b65`. The committed retirement verifier measures 40,260 candidate
CLI production lines versus 41,208 at baseline, a 948-line net reduction; the
deleted policy source is pinned at 287 lines and its recorded SHA-256.

The bounded `run.py` runner builds no provider connection and executes the
source-pinned shipped YUI with a credential-free environment. Report
`runs/session-terminal-kx8ca5pa/report.json` at candidate `5c423b65`
passes all four required cases:
replay completion, SIGINT user cancellation with partial output, provider
error, and the existing audio/tool replay. Each case records one terminal
diagnostic, final accounting, fixture/binary hashes, and reaped process-group
state; the tool replay retains the marker, strict continuation text, and the
expected `audio/out-000.pcm` SHA-256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`.

The exact earlier coverage rejection is preserved in
`ci-rejection-34737625163.json`; its declaration-only manifest repair is
`2ba46424`. The latest exact-head rejection is preserved in
`ci-rejection-34748383831.json`: eight required checks passed, while
`CI (integration)` failed only in `Run production-binary audio-device replay
integration` at the pre-merge head `a949546b`. Trial 05 rendered
171191/177591 compared samples, losing 6400 with 16 underflow events and 7360
zero-filled samples. The failure is in the peer/device audio drain path; the
C118 diff does not own the integration, device, transport, or gateway paths.
The independent review finding about the nil-error/no-continuation shortcut
was repaired in `4b4b10c1`; the merged head also receives the one-line
architecture-budget repair `9cf7a04`. Focused normal/race tests, the
GOWORK=off consumer, both causal mutants, retirement verification, the shipped
YUI workflows, and the accumulated normal/coverage/race session regression
matrix pass at the current candidate.

After fetching fresh accepted main, the isolated branch merged `09c70f51` as
`5c423b65` and reran the focused normal/race tests, external consumer, causal
mutants, retirement verifier, coverage registration, Wire, architecture-size,
vet, pinned staticcheck, pinned lint, and
`COUNT=3 scripts/test-session-ci-regressions.sh all`; all passed. The final
source-pinned shipped YUI artifact and report are rebuilt at `5c423b65`.

The evidence is vertical only. Focused normal/race tests, the accumulated
session regression matrix, pinned lint/staticcheck/vet, Wire generation and
architecture checks, the external consumer, both causal mutants, and the
shipped YUI workflows pass.
The fresh-main merge preserved predecessor checkpoints and did not raise an
architecture baseline or modify the shared registries directly. Script CI is
not being resubmitted unchanged after the recorded integration rejection:
primary/C64 ownership must transfer or land an accepted peer repair for the
audio-device underflow first. Then this same task must integrate the repaired
accepted main, rerun the focused and accumulated gates, push the changed
candidate, and submit it to script CI without polling. Independent review,
guarded merge, and the post-merge probe remain external handoff gates.
