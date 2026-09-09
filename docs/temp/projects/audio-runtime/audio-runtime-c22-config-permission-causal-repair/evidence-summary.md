# C22 causal repair evidence

- Work: `audio-runtime-c22-config-permission-causal-repair`
- Branch/worktree: `codex/audio-runtime-c22-config-permission-causal-repair`
- Fetched `origin/main`: `e4137eba6a6499142f50701609c1149afc71db84`
- Implementation checkpoint: `e927682a8e7da9912d3ea9fd2cfb2b01db50c653`
- Current candidate/evidence HEAD: `b8b446288c408b35cb74af70703efd831635bfbc`
- Required startup and baseline pins are ancestors of the candidate:
  `8bdafc7f947a3a2c9856220abdc539437035bd21` and
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.

## Causal baseline

`baseline.json` runs the unchanged
`TestConfigStorageCommitPreservesPermissionsAndPublishesAtomically` in two
fresh POSIX child processes and a diagnostic copy of the same source. On
`e4137eba`, the exact test passes under umask `022` and fails under `077` with
`config mode = 600, want 640`. The diagnostic copy records raw requested `0640`
as `0640` under `022` but `0600` under `077`, while explicit `chmod 0640`,
explicit `0600`, and absent-file default `0600` controls remain stable.

## Candidate evidence

- `candidate.json`: exact test passes under both child masks in 17.4s; all
  eight diagnostic mask/scenario observations pass.
- `public-config.json`: rebuilt same-source `yui` passes six bounded cases under
  `022` and `077`: existing `0640`, absent private `0600`, and probe-time stale
  revision conflict for each mask. Bytes, modes, success/error output and lock/
  temp cleanup are checked.
- `public-ask.json`: the same binary records a deterministic local SSE answer,
  stops and joins the helper, then replays the capture after helper shutdown;
  both children pass the 60-second bound.
- `yui` SHA256: `1bb25bcadcb578c854f62af5616ed583b660bf044ea94ce4ade7dd346b347739`.

## Rejected CI inspection

- Run `34372156060`, job `102535799797`, rejected PR #413 at this candidate
  head because `CI (hermetic)` failed in
  `TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls/missing_commit`.
  The full job log reports the positive harness baseline failing before the
  negative-control mutation: six crossings were recorded, harness B completed
  two turns, and its third turn reached the existing two-second deadline
  without final audio. This is the same canonical C09 integration signature
  recorded in `factory/docs/c09-operator-findings.md`.
- The candidate and fetched `origin/main` both pass the exact focused test in
  isolation (`CGO_ENABLED=0`, `-tags=nomicrophone`, `-count=1`, 30-second Go
  timeout), and `git diff origin/main...HEAD` has no changes under the failing
  integration/service paths. No C22-owned repair is indicated; changing the
  other task's integration path would violate this lease. The rejected run is
  retained as required-check evidence, not claimed green.

No live Realtime, credential, physical-device, or acoustic evidence is claimed.
The candidate is ready for executor handoff to script-owned current-head CI;
CI, independent review, guarded merge, and post-merge vertical acceptance are
not claimed by this evidence.

## C22 exact duplex failure ownership recovery

The PRD amendment assigns the named `agent-cli/test/integration/session_duplex_overlap*_test.go`
fixture paths to this task for the rejected hermetic baseline. The full saved
job log at `/tmp/audio-runtime-c22-hermetic-34372156060.log` showed six
crossings, a third transcript on harness B, and no final audio/terminal before
the unchanged two-second harness deadline. The fixture's final bridge writer
published its EOF packet and then synchronously waited for the peer reader's
`eofSeen`; that can deadlock final replay response completion because the peer
may only reach its reader after that response returns.

The candidate changes only the owned bridge fixture: EOF publication remains
gated by the existing scheduled `eofReady`, packet delivery and abort paths,
but the post-publication `eofSeen` observation is now opportunistic rather than
blocking. All original command and harness deadlines, parallel overlap
semantics, negative controls, and PCM/transcript/commit/terminal assertions
remain unchanged. The architecture-size gate passes without changing the
immutable migration baseline: 181 packages, 1860 files and 27151 functions.

Current candidate source revision for refreshed evidence is the committed
`d78e7b8f7d9f40a3cd15b3a98dfa8b411f72806e`. Final focused proof passed the complete
`TestSessionCLI_DuplexPCMMultiTurn*` family 5/5 in normal mode and once under
`-race`, plus the same family under `GOMAXPROCS=1`, `GOMAXPROCS=8`, and
`CGO_ENABLED=0 -tags=nomicrophone`. Config evidence also passed the candidate
022/077 permission matrix (exact test and all eight diagnostics), config normal
and nomicrophone tests, config race, and config vet. Refreshed same-source
public config (six cases across both masks) and credential-free local ask/
replay (record then replay after helper shutdown) both passed; the yui binary
SHA256 is `8ebeff1fa864d67873d0091f4c38bbd5850fb8cff6dd4c1827622c0728bc022a`.

The implementation is committed as `d78e7b8f7d9f40a3cd15b3a98dfa8b411f72806e`
and the evidence-provenance follow-up is committed/pushed as
`25bda944`. The rejected CI run remains historical evidence and is not called
green. No terminal CI polling, independent review, merge, or post-merge
vertical acceptance is claimed; next action is the script-owned current-head
CI gate.
