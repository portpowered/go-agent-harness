## Summary

- Move room replay bundle contract, manifest parsing, safe path resolution,
  inventory/digest validation, capture/timeline admission, typed errors, and
  output exclusion into `go-agent-runtime/services/roomreplaybundle`.
- Keep the CLI room replay files as deprecated aliases/compatibility helpers;
  room scheduling, runtime execution, and recording orchestration are unchanged.
- Add dedicated runtime Wire construction, coverage registration, bounded-read
  regressions, and a separate `GOWORK=off` public consumer.

## Evidence

- Runtime normal/race: 49 tests in 3 packages; the owned internal service
  coverage repair passes 47 package tests at 80.9% local statement coverage
  against the 80% floor.
- CLI room replay package: 1,057 tests; focused CLI race: 8 tests.
- `go vet`, pinned golangci-lint 2.9.0, `staticcheck 2026.1`, coverage registration, and diff check pass.
- External consumer prints `C75_ROOMREPLAYBUNDLE_CONSUMER PASS` and rejects a
  same-length artifact corruption with `ErrInvalidRoomReplayBundle`.
- Legacy named CLI production files: 1,899 lines at admitted main; current
  compatibility files: 313 lines; 1,586 production lines retired.
- Current exact evidence head `9f4af321087e4c3dbd588486c69d83004320fa8a`
  descends from the owned coverage repair `17fa4d384f60343480daacc753ebfcc8d155d049`
  and includes the architecture-budget test split, alongside source repair
  `c3e8f8901d802dad74d52acb7006551f88171d66`
  and includes freshly fetched `origin/main`
  `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`. The repair handles close errors,
  checked JSON/type assertions, malformed timing errors, and the prior
  roundtrip-test budget growth.
- Accumulated session regression controls at `COUNT=1` pass in normal,
  coverage, and race modes, including the expected mismatch, PCM/transcript,
  and tool-continuation negative controls.
- The provided bounded C75 runner passes at `9f4af321`, including
  runtime normal/race, CLI compatibility, vet, staticcheck, coverage
  registration, the GOWORK=off consumer, and diff check. The accumulated
  session regression script passes all normal/coverage/race packages at
  `COUNT=1`.

- The current GitHub CI run `34676139982` at the prior head rejected coverage
  at 75.40% for the owned internal service and reported the exact peer-owned
  `test46/slow_device` timeout. The coverage repair is locally verified at
  80.9%; the focused slow-device reproduction passes 6/6 repetitions. No
  peer runtime/audio source was changed.

## Prior CI repair

The earlier PR #465 head `9543afb2` static job in run `34674059055` failed on
the unregistered generated Wire file, 35 expected C75 shared baseline findings,
and three unchecked type assertions in the roundtrip test. The owned source and
test repairs are now locally validated; no current-head CI result is claimed.

## Shared gate handoff

`wire-check` and `architecture-size-check` are recorded with their exact
failures because the new Wire package and moved complexity require downward
updates to `scripts/wire-packages.txt` and
`docs/architecture/architecture-size-baseline.json`. Those files are held by
the active C57/C61 leases and were not edited. The exact next action is to
request only those demonstrated downward registrations after the owners release,
then rerun the two gates on this same branch before independent review.

No CI, review, merge, physical-device, or acoustic claim is made here.
