# audio-runtime-c70-retire-cli-metrics-replay evidence ledger

This is implementation evidence for the admitted `audio-runtime` task. It is
not script-CI, independent-review, merge, immutable-vertical, or project
acceptance evidence.

## Admission and ancestry

- `project-control.py verify-work --type task --name audio-runtime-c70-retire-cli-metrics-replay` returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c70-retire-cli-metrics-replay"}`.
- The admitted manifest is the repository-root `prd.json`. Its `branchName` is `codex/audio-runtime-c70-retire-cli-metrics-replay`, matching this isolated worktree.
- The startup integration revision is `8bdafc7f947a3a2c9856220abdc539437035bd21`; planning and fetched `origin/main` are `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`.
- Candidate checkpoint `8da976ad905fbc8c1f7136d5c708bf04bf0d555c` is pushed to the matching branch; startup integration, planning main, and fetched `origin/main` are all ancestors. The running host checkout was never merged or reset.
- The canonical board admitted task row `work-task-82` in `init`, with no C70 review or rejection row. Existing predecessor checkpoints and leases remain outside this change.

## Scoped implementation

- The 159-line accepted-main `metrics_collector.go` was reduced to 137 production lines: net retired CLI production lines `22`.
- The adapter now only maps `SessionRuntimeFactory`/`RunSession`, the existing in-memory sink, fixture loader, and legacy probe series. JSON parsing, base64 accounting, tool deduplication, reconciliation, projection, cancellation, and sink lifecycle are owned by `go-agent-runtime/services/metricsreplay`.
- The new service has a public contract, private implementation, explicit dependency seams, and generated dedicated Wire construction. The existing probe interface, probe transport, and agent-cli Wire callers are unchanged.
- Changed paths are confined to the four admitted prefixes: the legacy collector, `go-agent-runtime/services/metricsreplay/`, its coverage manifests, and this evidence directory.

## Exact source census

- Accepted-main collector blob: `88e18b96723eff018787c815de8e677d40705a1a`; current collector blob: `b46d3fcf950886eacd82b7d2eb9d4a1443073781`.
- `NewMetricsCollector` is at line 20; the only adapter `Collect` call is line 34. The adapter's injected runner/loader/sink declarations are `sessionMetricsReplayRunner` line 50, `sessionMetricsFixtureLoader` line 78, `newSessionMetricsSink` line 96, and the legacy series mapping is lines 38–45.
- The only production collector call-site outside the owned file is `agent-cli/internal/services/wire/metrics.go:13`; the root Wire provider remains `agent-cli/internal/wire/wire.go:306`. The existing probes interface and transport are unchanged.
- Forbidden-import census over `go-agent-runtime/services/metricsreplay/**/*.go` returned no matches for `agent-cli`, `internal/services/internal/agentruntime`, `go-agent-loop/pkg/metrics`, or `go-llm-gateway`.

## Focused gate evidence

The following commands passed against the same source before checkpointing:

- `rtk make fmt`
- `rtk make vet`
- `rtk make lint` using pinned golangci-lint `v2.9.0`
- `rtk make staticcheck` using pinned staticcheck `2026.1`
- `rtk go test ./go-agent-runtime/services/metricsreplay/...`
- `rtk go test -race ./go-agent-runtime/services/metricsreplay/...`
- `rtk go test -coverprofile=/tmp/audio-runtime-c70-metricsreplay.cover ./go-agent-runtime/services/metricsreplay/...`: 50 tests; total `85.8%`
- public package coverage: `100.0%` (floor `90.00%`)
- private service coverage: `85.4%` (floor `80.00%`)
- generated Wire package coverage: `100.0%` (floor `95.00%`)
- `rtk go test ./agent-cli/internal/services/internal/agentruntime ./agent-cli/internal/services/wire`
- `rtk go test ./agent-cli/internal/services/internal/agentruntime -run 'TestSessionMetricsMatrix' -count=1`
- `rtk go test -race ./agent-cli/internal/services/internal/agentruntime -run 'TestSessionMetricsMatrix' -count=1`
- `rtk go test ./agent-cli/test/integration -run '^TestSessionCommand(MetricsReconcileMatchesIndependentFoldOverFullSession|MetricsReconcileMissingOutputTextDeltaFails|FinalAccountingIsEmittedOnceOnError)$' -count=1`
- `rtk go test ./agent-cli/test/integration -run '^TestSessionCommand_(HelpDocumentsRecordReplayAndHistorySubcommands|OpenAIRealtimeReplay_EndToEndSmoke)$' -count=1`
- `rtk go test ./agent-cli/test/integration -run '^TestReadImageSpokenFailedContinuationIsActionable$' -count=1 -timeout 480s`
- `rtk go test ./agent-cli/internal/transport/cli -run 'TestProbeRunS2SV7A(MetricsModalityReconcilesOffline|OvercountFailsNamingOutputTool)$' -count=1`
- `GOWORK=off rtk go test ./...` from `evidence/consumer`
- `GOWORK=off rtk go test -race ./...` from `evidence/consumer`
- `rtk make coverage-registration`
- `rtk make coverage-changed COVERAGE_BASE=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`

The service package coverage profile measured `85.8%` in aggregate; its
per-package floors are all met as recorded above. The positive v7a workflow
reconciles; its deliberate output/tool overcount remains a failing negative
control naming `output/tool` and `16` vs `17`.

## Shared prerequisites still held

The new generated graph is intentionally not added by this task to the leased
shared registry. Consequently:

- `rtk make wire-check` reports only `unregistered=['go-agent-runtime/services/metricsreplay/wire/wire_gen.go']`.
- `rtk make architecture-size-check` reports only the two stale accepted-main
  complexity entries for the retired helper and the missing generated-file
  registration for the new graph.

After the canonical lease release or explicit amendment, the next owner action
is to add the metricsreplay Wire path to `scripts/wire-packages.txt`, register
the generated graph in the architecture policy, delete the two demonstrated
C70 stale baseline entries, rerun `wire-check` and `architecture-size-check`,
then update the same pushed PR and submit the changed head to script CI. No CI,
review, merge, device/acoustic, or project-completion claim is made here.
