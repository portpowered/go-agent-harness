# C114 audio-rate contract checkpoint

This is the admitted `audio-runtime` task `audio-runtime-c114-retire-cli-audio-rate-contract` on branch `codex/audio-runtime-c114-retire-cli-audio-rate-contract`, pushed checkpoint `3cc1764098975476e1a923094b98879de9fe1b82`.

Executor resumption verification at the current branch head
(`008ad4a44c4d956b64c1ed4c76acb672e8d0328d`): admission remains valid, the
branch still matches `prd.json`, and freshly fetched `origin/main` remains
`3963bc3566da24f8214634c17a9d0f79a6724171`. The implementation source is
unchanged from `3cc1764098975476e1a923094b98879de9fe1b82`; this refresh is
evidence-only.

Admission and ancestry were verified in the isolated worktree from the
immutable project manifest:

- `project-control.py verify-work --type task --name audio-runtime-c114-retire-cli-audio-rate-contract` returned `admitted`.
- The branch matches `prd.json` exactly.
- `origin/main` and the candidate base are `3963bc3566da24f8214634c17a9d0f79a6724171`.
- Startup `8bdafc7f947a3a2c9856220abdc539437035bd21`, accepted/planning main, and freshly fetched `origin/main` are ancestors.
- `progress.txt` has no C114 checkpoint, and the canonical board has no C114 review row or rejection feedback.

The implementation moves PCM16 conversion, scheduled-input conversion, rate
resolution, and session configuration behind
`go-agent-runtime/services/audiorate`. The public package contains only the
host-neutral contract and stable error/rate declarations. The private service
uses `go-audio/pkg/codec` and `go-audio/pkg/wavio`; the generated Wire graph is
under `go-agent-runtime/services/audiorate/wire`. The two legacy files retain
only deprecated aliases and decision-free adapters, and existing callers were
not edited.

The focused service suite covers exact 16/24/48 kHz sample counts, no-op
backing identity, PCM16 odd-tail and unsupported-rate errors, scheduled order
and metadata, replay/live/default resolution, setter effects, pre-side-effect
validation, and cancellation. The separate `external-consumer` imports only
the public audiorate/Wire contract and go-audio and is run with `GOWORK=off`.

Passed local evidence:

- audiorate normal and race tests, focused CLI audio/rate/replay/scheduled tests, and the GOWORK-off consumer;
- bounded `verify.py` positive/causal-negative, retirement/C101-read-only, and final-scope modes;
- bounded `run.py` cases `c21-rate-consumption`, `c50-audio-tool-replay`, and `room-scheduled-audio`;
- follow-up `9d07de6608f38219f83ed2491d18d9282799137f` repaired the retirement verifier's false C101 match on its own evidence filename; all three verifier modes pass on the committed candidate;
- C21 runtime/device sink consumption boundaries passed in normal and race modes; these are simulated callback/software observations, not physical or acoustic proof;
- `make fmt`, `make vet`, pinned `make lint` (golangci-lint v2.9.0), pinned `make staticcheck` (2026.1), `make size-check`, `make coverage-registration`, and the architecture-gate unit tests;
- `git diff --check`.

The resumed focused checks pass: audiorate normal (21 tests), audiorate race
(63 tests across three repetitions), the GOWORK-off external consumer, focused
CLI audio/rate/replay/scheduled regressions (30 tests), all three bounded
C21/C50/scheduled cases, and the accumulated normal/coverage/race session
regression matrix at `COUNT=1` (including high-rate and expected-negative
controls). The non-shared quality checks `make fmt`, `make vet`, pinned
`make lint`, pinned `make staticcheck`, `make size-check`,
`make coverage-registration`, and `make test-architecture-gate` also pass.

The architecture check reports one shared prerequisite only:
`generated-file-spoof services/audiorate/wire/wire_gen.go`. C79 currently owns
the shared `scripts/wire-packages.txt` registry and
`docs/architecture/architecture-size-baseline.json` lease while its changed-head
script CI is running. Those files are intentionally untouched. After C79 explicitly releases
the lease, fetch and reconcile current main in this isolated branch, register
the generated graph, rerun the final gates, then commit/push and submit this
same task to script CI. No CI, review, merge, or broad project acceptance is
claimed by this checkpoint.

The exact requested `$FACTORY_ROOT/factory/docs/implementation-handoff.md` is
absent from the admitted checkout and available Git history; the canonical
`operating-policy.md`, `handoff-plan.md`, `meta-planner-handoff.md`, and
implementer workstation instructions were read instead. The strict handoff
therefore remains blocked pending restoration of that required document and
explicit C79 release of the shared paths. Do not modify either shared file or
submit this known-red architecture candidate. Once both prerequisites are
available, fetch/reconcile `origin/main`, add only the audiorate Wire and
downward baseline registrations, rerun Wire/architecture/coverage and the
focused gates, then update PR #499 and submit the same task to script CI
without polling it.
