# C82-04 CLI retirement checkpoint

At the accepted-main baseline, the two owned production files were 1,061 and
72 lines (1,133 total). The final candidate is:

- `session_live.go`: 574 lines.
- `session_live_setup.go`: 54 lines.
- Combined: 628 lines.
- Retired: 505 physical lines, above the 500-line floor and below the 633-line
  ceiling.

The CLI retains caller-owned provider/record/replay/browser selection and
presentation policy. The deleted legacy stream runner and termination boundary
are replaced by the thin `sessionlive` adapter and dedicated Wire construction.

Controls passed:

- Focused normal CLI session/scheduled/liveness suite: 282 tests.
- Focused CLI race suite: 72 tests.
- Bounded audio/tool continuation controls: passed.
- Bounded replay-bypass controls: passed.
- Source-pinned `yui` build and credential-free `yui --help` process smoke:
  passed.
- `verify.py --mode retirement-and-scope`: passed with no excluded or peer path
  changes.
