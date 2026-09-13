# C118 session-terminal retirement evidence

`inventory.json` records the admitted project, immutable source baseline, dual
ancestry, caller census, peer lease exclusions, and the current task-board
CI/review provenance. The current task is `work-task-23`. Accepted peer repair
main `915ed982d` and the subsequent accepted main `b8650efd` are integrated by
merge `020f665f6`; the tested source and source-pinned artifact are
`020f665f6`. The independent review finding about
scheduled-incomplete nil/context failures was repaired in `4b4b10c1`, with the
architecture-budget repair in `9cf7a04`.

The current-main coverage registration repair is deliberately narrow: the
released C127 causal package contains only tests, so
`coverage-manifest/go-agent-runtime/services/session/internal/live/causal/package.json`
registers it with the existing `test-only` exception. No C127 production code,
device path, transport path, shared Wire registry, or architecture baseline was
modified by C118.

`external-consumer` is a separate Go module. With `GOWORK=off`, its test imports
only the public `go-agent-runtime/services/sessionterminal` contract, its
generated Wire constructor, and the public provider taxonomy. It verifies typed
error identity, deterministic continuation metadata, accounting,
cancellation/output-state policy, and independent service construction.

The committed retirement verifier measures 39,329 candidate CLI production lines
versus 41,208 at the pinned baseline, a 1,879-line net reduction; the deleted
policy source is pinned at 287 lines and its recorded SHA-256.

The bounded `run.py` runner builds no provider connection and executes the
source-pinned shipped YUI from tested source `020f665f6` with a credential-free
environment. Report `runs/session-terminal-izq_fvua/report.json` passes all
four required cases with YUI SHA-256
`908f7744c0bb7ec4678f721f875e3ee91621ef7922443be4fed7fdb47ec59c00`:

- replay completion: exit 0, `replay_complete/replay/complete`, two accounting
  records, rendered PCM 3,360 bytes with the healthy 2,400-byte tail;
- SIGINT user cancellation: exit 0, `user_cancelled/cancellation/cli/partial`,
  1,440 provider audio bytes and partial output;
- provider error: exit 1, `terminal_failure/terminal_failure/session/none`,
  incomplete accounting;
- existing audio/tool replay: exit 0, `provider_close/provider_close/provider/not_applicable`,
  `PROBE_TOOL_MARKER_9182`, strict continuation, and the expected provider PCM
  SHA-256 `0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`.

All cases reaped their process groups. The credential-free external consumer,
both causal mutants, named normal/race tests, retirement verifier, accumulated
normal/coverage/race session regression matrix at `COUNT=3`, coverage
registration (192 packages), Wire, architecture-size (202 packages, 1,940
files, 28,756 functions), vet, pinned staticcheck, pinned lint, formatting and
diff checks pass at the tested source.

The preserved exact-head rejection `ci-rejection-34748383831.json` recorded
eight green checks and one `CI (integration)` failure in the peer production
audio-device drain path: `test45/trial_05` lost 6,400 of 177,591 compared
samples with 16 underflows and 7,360 zero-filled samples. C127 repaired that
peer path in accepted main `915ed982d`; C118 integrated accepted main through
`b8650efd` and reran its bounded gates. No current-head script CI result,
independent review, guarded merge, post-merge vertical probe, physical/acoustic
proof or project acceptance is claimed.

Next action: commit and push this refreshed same-task candidate, update PR #504
with the exact merge, coverage-registration repair, test and artifact evidence,
then return `ACCEPTED` to the script-owned changed-head CI gate without polling.
Retain C118 ownership for any exact CI rejection or actionable review repair;
independent review, guarded merge and the post-merge vertical probe remain
external gates.
