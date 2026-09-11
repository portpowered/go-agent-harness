# C50 remaining CLI service inventory

This directory is evidence-only for `audio-runtime-c50-remaining-cli-service-inventory`.
The inspected source is the exact integrated `origin/main` revision
`7f73c8b3b4ebc99b55b8bb5e802beff024385407`; the startup integration checkpoint
is `8bdafc7f947a3a2c9856220abdc539437035bd21`. The branch is
`codex/audio-runtime-c50-remaining-cli-service-inventory`.

The task-local Go AST/import/call-site analyzer enumerates 120 production files,
46,583 physical lines and 2,869 top-level symbols across the three admitted
CLI roots. It records 143 excluded files, 55,242 excluded physical lines and
3,183 direct production call edges. The historical `107 files / 41,305 lines`
figure is reconciled as the older agentruntime-only scope: the current admitted
scope is 107 agentruntime files plus 9 livehost files plus 4 room files.

Every symbol has exactly one classification. The analyzer is conservative:
unresolved interface, reflection, generated-code and dynamic registration paths
are recorded as `DEAD_OR_UNCERTAIN`; AST selector references are not presented
as proven calls. Source files, tests, generated files, fixtures and prior
evidence are read-only inputs. `analysis/` contains the pinned inventory,
classification ledger and call-path ledger; `SHA256SUMS` binds each input and
report after the final run.

The two selected future slices are `room-document-admission` and
`room-participant-mesh`. Their current writer paths and proposed
`services/rooms`, `services/rooms/internal`, and `services/rooms/wire` writer
paths are pairwise disjoint. Room mixer/audio-out, terminal policy/livehost
request, Python probe tooling, and runtime-live/coordinator paths are excluded
for C38/C46/C47/C48 ownership. Native Windows hardware/endpoints and physical
acoustics are out of scope; no Realtime credential or live provider is used.

Run the bounded evidence driver from the repository root:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c50-remaining-cli-service-inventory/verify.py --mode all
```

It refreshes `origin/main`, verifies admission and ancestry, checks source
cleanliness outside this directory, reruns the analyzer and determinism check,
builds the same-source `yui`, exercises top-level and session help plus the
credential-free C16 local replay, runs focused normal/race tests, and records a
timeout/process-group negative control. Each child is capped at 60 seconds and
the aggregate driver is capped at 600 seconds. The nine project acceptance
criteria remain OPEN; this report is an inventory and handoff, not a migration
percentage or an acceptance waiver.
