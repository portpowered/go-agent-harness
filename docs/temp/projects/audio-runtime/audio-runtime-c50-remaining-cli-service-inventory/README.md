# C50 remaining CLI service inventory

This directory is evidence-only for `audio-runtime-c50-remaining-cli-service-inventory`.
The inspected source is the clean integrated `origin/main` snapshot
`2456a5d1594e73faf85e1132d6050735bc3e4710`, which is a descendant of the
planning snapshot `7f73c8b3b4ebc99b55b8bb5e802beff024385407`. The startup
integration checkpoint is `8bdafc7f947a3a2c9856220abdc539437035bd21`.
The branch is
`codex/audio-runtime-c50-remaining-cli-service-inventory`.

The final evidence refresh is bound to the candidate HEAD recorded in
`provenance.json` and `verification-summary.json`. The prior script-CI run
at that candidate passed every required job except hermetic, whose complete log
failed only in the unrelated main-line room lifecycle test
`TestRunnerRetainsTypedSilentTerminalAndIsolatesPeer` with
`silent_provider_empty_response` at
`go-agent-runtime/services/rooms/internal/lifecycle/runner_test.go:226`.
The bounded focused reproduction passed locally. C50 has a docs-only write
lease, so no production or peer-task repair is authorized for that failure;
it remains explicit CI evidence for the script-owned resubmission.

The task-local Go AST/import/call-site analyzer enumerates 120 production files,
46,563 physical lines and 2,875 top-level symbols across the three admitted
CLI roots. It records 143 excluded files, 55,405 excluded physical lines and
3,869 direct production call edges. Receiver-typed method calls resolve to
exact declarations, including `Mesh.Join`, `Mesh.Remove`, `Mesh.Peers`, and
`Mesh.Close`. Public entry paths are exact `.go` files immediately inside the
declared CLI source roots; nested `internal/livehost` implementation files are
not public entries. The historical `107 files / 41,305 lines`
figure is reconciled as the older agentruntime-only scope: the current admitted
scope is 107 agentruntime files plus 9 livehost files plus 4 room files.

Every symbol has exactly one classification. The analyzer is conservative:
unresolved interface, reflection, generated-code and dynamic registration paths
are recorded as `DEAD_OR_UNCERTAIN`; declarations and type-only references are
not presented as calls. Source files, tests, generated files, fixtures and prior
evidence are read-only inputs. `analysis/` contains the pinned inventory,
classification ledger and call-path ledger; `SHA256SUMS` binds each task-local
input and report after the final run. The summary is explicitly excluded from
that manifest because it records the manifest digest itself.

The two selected future slices are `room-document-admission` and
`room-participant-mesh`. Their current writer paths and proposed
`services/rooms`, `services/rooms/internal`, and `services/rooms/wire` writer
paths are pairwise disjoint. Room mixer/audio-out, terminal policy/livehost
request, Python probe tooling, and runtime-live/coordinator paths are excluded
for C38/C46/C47/C48 ownership. Native Windows hardware/endpoints and physical
acoustics are out of scope; no Realtime credential or live provider is used.

Run the bounded evidence driver from the repository root:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c50-remaining-cli-service-inventory/verify.py --mode all --child-timeout 60 --total-timeout 600
```

It refreshes `origin/main`, verifies admission and ancestry, checks source
cleanliness outside this directory, reruns the analyzer and determinism check,
builds the same-source `yui`, exercises top-level and session help plus the
credential-free C16 local replay, runs focused normal/race tests, and records a
timeout/process-group negative control. Each child is bounded by
`--child-timeout`, all phases share `--total-timeout`, and the owned evidence
tree has a fixed output quota. `--binary PATH` is accepted for script handoff
and checked against a fresh same-source build. The nine project acceptance
criteria remain OPEN; this report is an inventory and handoff, not a migration
percentage or an acceptance waiver.
