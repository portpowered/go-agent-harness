# C118 session-terminal retirement evidence

`inventory.json` records the admitted project, immutable source baseline,
dual-ancestry checks, caller census, peer lease exclusions, and the current
task-board CI rejection/repair provenance. The current task is `work-task-23`,
and its clean fresh-main integration is `ec3de1bc` over `origin/main`
`ea53be13`. No independent C118 review finding exists.

`external-consumer` is a separate Go module. With `GOWORK=off`, its test imports
only the public `go-agent-runtime/services/sessionterminal` contract, its
generated Wire constructor, and the public provider taxonomy. It verifies
typed error identity, deterministic continuation metadata, accounting,
cancellation/output-state policy, and independent service construction.

Implementation checkpoints are `360d2a9`, `26365f5`, `55a652f`, and the
documentation/evidence descendants `084de02` and `9a44039`. The committed
retirement verifier measures 40,981 candidate CLI production lines versus
41,208 at baseline, a 227-line net reduction; the deleted policy source is
pinned at 287 lines and its recorded SHA-256.

The bounded `run.py` runner builds no provider connection and executes the
source-pinned shipped YUI with a credential-free environment. Report
`runs/session-terminal-y4m5bp0m/report.json` passes all four required cases:
replay completion, SIGINT user cancellation with partial output, provider
error, and the existing audio/tool replay. Each case records one terminal
diagnostic, final accounting, fixture/binary hashes, and reaped process-group
state; the tool replay retains the marker, strict continuation text, and the
expected `audio/out-000.pcm` SHA-256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`.

The exact-head CI rejection is preserved in
`ci-rejection-34737625163.json`. All other required checks passed; coverage
failed because the public contract package has no executable statements. The
manifest-only repair commit `2ba46424` changes its registration to the
declaration-only exception already used by the recording and devices public
contracts. Focused normal/race tests, the GOWORK=off consumer, both causal
mutants, retirement verification, and the accumulated normal/coverage/race
session regression matrix pass after the repair. The shipped artifact is
unchanged and remains SHA-256
`06a497e0467f009f32dac20984500b11661272addad719d5941228fe096f20ca`.

After fetching fresh accepted main, the isolated branch merged `ea53be13` as
`ec3de1bc` with no conflicts and reran the focused normal/race tests, external
consumer, causal mutants, retirement verifier, coverage registration, Wire,
architecture-size, vet, pinned staticcheck, pinned lint, and
`COUNT=3 scripts/test-session-ci-regressions.sh all`; all passed. The inherited
C107 evidence is the only fresh-main delta, so the source-pinned shipped YUI
artifact remains valid without relabeling.

The evidence is vertical only. Focused normal/race tests, the accumulated
session regression matrix, pinned lint/staticcheck/vet, Wire generation and
architecture checks, the external consumer, both causal mutants, and the
shipped YUI workflows pass.
After the released main integration, only the demonstrated generated-file
registration and stale retired-source fragment were applied; no baseline
ceiling was raised. Script CI, independent review, guarded merge, and the
post-merge probe remain external handoff gates.
