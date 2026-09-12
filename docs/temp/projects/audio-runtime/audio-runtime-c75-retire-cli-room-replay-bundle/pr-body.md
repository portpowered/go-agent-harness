## Summary

- Move room replay bundle contract, manifest parsing, safe path resolution,
  inventory/digest validation, capture/timeline admission, typed errors, and
  output exclusion into `go-agent-runtime/services/roomreplaybundle`.
- Keep the CLI room replay files as deprecated aliases/compatibility helpers;
  room scheduling, runtime execution, and recording orchestration are unchanged.
- Add dedicated runtime Wire construction, coverage registration, bounded-read
  regressions, and a separate `GOWORK=off` public consumer.

## Evidence

- Runtime normal/race: 21 tests in 3 packages.
- CLI room replay package: 1,057 tests; focused CLI race: 8 tests.
- `go vet`, pinned golangci-lint 2.9.0, `staticcheck 2026.1`, coverage registration, and diff check pass.
- External consumer prints `C75_ROOMREPLAYBUNDLE_CONSUMER PASS` and rejects a
  same-length artifact corruption with `ErrInvalidRoomReplayBundle`.
- Legacy named CLI production files: 1,899 lines at admitted main; current
  compatibility files: 313 lines; 1,586 production lines retired.
- Current candidate `c3e8f8901d802dad74d52acb7006551f88171d66` includes freshly
  fetched `origin/main` `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`. The repair
  handles close errors, checked JSON/type assertions, malformed timing errors,
  and the prior roundtrip-test budget growth.

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
