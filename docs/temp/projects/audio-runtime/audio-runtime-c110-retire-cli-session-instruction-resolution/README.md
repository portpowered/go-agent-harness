# C110 session instruction resolution evidence

This is the admitted evidence folder for
`audio-runtime-c110-retire-cli-session-instruction-resolution`.

The `consumer` directory is a separate Go module. It imports only the public
`services/sessioninstructions` contract and its dedicated Wire constructor,
then exercises literal, file, `AGENTS.md`, explicit `none`, scope, skill-order,
composition, invalid-loader, and cancellation behavior. It does not import the
CLI, private runtime packages, credentials, or ambient filesystem state.

The canonical implementation is stateless and receives all host I/O through an
injected loader. Resolution rejects malformed or oversized text, preserves
causes with phase-specific `ResolutionError` values, checks cancellation around
loader calls, and clones capability snapshots before policy composition. The
public `sessioninstructions` package is contract-only; its unexported
implementation lives under `internal/service`, and the dedicated instruction
Wire owns construction. The session service Wire does not import a peer Wire;
the existing CLI construction callers use the dedicated instruction Wire
directly. The external consumer constructs two independent public services and
verifies that their loader state does not cross-contaminate.

The committed verification command is:

```text
./verify.py --mode positive-and-guard-mutations
./verify.py --mode retirement-and-owned-paths --report
./run.py
```

It runs the consumer with `GOWORK=off`, checks direct imports and the thin CLI
adapter, and records bounded JSON reports under `reports/` when reports are
requested. `run.py` builds the `nomicrophone` YUI artifact, runs the source-pinned
instruction matrix, replays the accepted C16 audio/tool fixture with and without
the text seed, and records the malformed/oversized pre-provider regression. Its
child processes are process-group bounded and credential environment variables
are removed. These artifacts are implementation evidence only; they do not
claim CI, review, merge, or project-wide acceptance.

## Current implementation handoff

The exact current executable-input checkpoint is
`95aa12c801aed0a094d1dfa9078a2e819658b356`, with
`origin/main=915ed982d23f2e549e529ff43c4f370b4b51e394`. The bounded `run.py`
replay was accepted in
`runs/run-20260913T162432Z-33439`; its nomicrophone YUI artifact is
`b8f12ab21d7a8a14ebe200a7c0ea2e96e19ce21bf36700d354a892c2d9bc2a2f`, and
the artifact, provenance, and run summary all bind to that tested source
revision. The lifecycle repair prepares the capability coordinator before
instruction resolution, so malformed resolution joins the cleanup error and
closes the caller-owned capability exactly once; the focused regression also
asserts that provider connection is never attempted. The subsequent evidence
ledger commit is a docs-only descendant with unchanged executable-input
hashes. The positive instruction/text-seed path, invalid pre-provider path,
clean shutdown, and C16 audio/tool replay are recorded there.

Focused session-instruction normal/race, CLI normal/race, the one-update
provider regression, consumer, mutation, retirement, Wire,
architecture-size, coverage-registration, accumulated normal/coverage/race
regressions, vet, pinned staticcheck, pinned lint, format, and diff gates
passed. The known peer high-rate audio/tool finding and the current
peer-owned Windows audio-portable CI failure remain preserved as external
evidence; neither is waived or relabeled as a C110 defect. This exact-source
evidence checkpoint is handoff evidence, not CI/review/merge approval.
