# C114 audio-rate contract checkpoint

This is the admitted `audio-runtime` task `audio-runtime-c114-retire-cli-audio-rate-contract` on branch `codex/audio-runtime-c114-retire-cli-audio-rate-contract`, implementation checkpoint `94da4c489e8211dce77088efae78bbe91d0e895b`.

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
validation, and cancellation. The separate consumer imports only the public
audiorate/Wire contract and go-audio and is run with `GOWORK=off`.

Passed local evidence:

- audiorate normal and race tests, focused CLI audio/rate/replay/scheduled tests, and the GOWORK-off consumer;
- `make fmt`, `make vet`, pinned `make lint` (golangci-lint v2.9.0), pinned `make staticcheck` (2026.1), `make size-check`, `make coverage-registration`, and the architecture-gate unit tests;
- `git diff --check`.

The architecture check reports one shared prerequisite only:
`generated-file-spoof services/audiorate/wire/wire_gen.go`. C79 currently owns
the shared `scripts/wire-packages.txt` registry and
`docs/architecture/architecture-size-baseline.json` lease while its PR is in
review. Those files are intentionally untouched. After C79 explicitly releases
the lease, fetch and reconcile current main in this isolated branch, register
the generated graph, rerun the final gates, then commit/push and submit this
same task to script CI. No CI, review, merge, or broad project acceptance is
claimed by this checkpoint.
