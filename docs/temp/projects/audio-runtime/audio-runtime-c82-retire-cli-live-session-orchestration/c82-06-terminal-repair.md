# C82-06 terminal recovery and rejection repair checkpoint

Candidate repair is based on `27bc7e1f598c5e23aef08fcb9af01817011fecda`
(`feat: extract live session orchestration service`) and remains on the
admitted branch. The pre-repair candidate reproduced the historical coverage
failure: the paced three-frame audio oracle failed 8/50 times without
coverage and 4/30 times with coverage. The detached predecessor at
`27bc7e1f^` passed the same oracle 50/50.

The repair keeps caller-owned finite input producers alive through the bounded
provider straggler drain, then cancels and joins input before owned-resource
cleanup. This preserves the extracted service's one terminal boundary and
keeps process-owned blocking input cancellation within the same bounded
cleanup path. A service regression test now asserts cancellation ordering.

Current focused evidence:

- service cancellation-order test: normal 20/20, race 5/5;
- paced three-frame audio oracle: normal 50/50 and coverage-instrumented
  50/50;
- focused audio cancellation control: normal 20/20, race 10/10;
- C82 service package: normal and race pass;
- full agent-runtime package: normal pass; focused C82 race regressions pass;
- independent external consumer: normal and race pass with `GOWORK=off`;
- `make fmt`, `make vet`, pinned golangci-lint, pinned staticcheck, and
  coverage registration (179 packages across 6 modules) pass;
- behavior matrix, both mutation controls, and retirement-and-scope pass;
- retirement remains `session_live.go` 574 + `session_live_setup.go` 54,
  total 628, with 505 lines retired.

The exact rejected findings are repaired: the three unchecked test closes and
the magic timeout are gone, and the now-unreferenced
`flushBufferedSessionLoopMessages` helper is deleted after proving there are
no callers. The evidence verifier's allow-list names that one explicitly
authorized legacy path exactly; no broad scope was added.

Shared-lane prerequisites remain unmodified and are not waived:

- `make wire-check` reports only the unregistered generated file
  `go-agent-runtime/services/sessionlive/wire/wire_gen.go`; the exact
  `scripts/wire-packages.txt` writer remains leased to C79.
- `make architecture-size-check` reports the ten expected reduced/stale
  `session_live.go` entries plus the newly reduced `session_drain.go` file
  baseline; `docs/architecture/architecture-size-baseline.json` remains
  leased to C79.

A broad local race sweep also timed out in the unchanged one-second browser
validator test; the focused C82 race set is green and no unrelated test or
fixture was changed.

Next action: commit and push this disjoint repair on the same branch/PR. After
C64's terminal-drain work and C79's shared-lane lock release reconcile on
accepted main, apply only the generated Wire registration and demonstrated
downward/stale baseline entries, rerun both shared gates, and submit the same
PR to script CI without polling it.
