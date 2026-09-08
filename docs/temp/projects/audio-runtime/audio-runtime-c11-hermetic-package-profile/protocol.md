# C11 hermetic package profiling protocol

Task `audio-runtime-c11-hermetic-package-profile` is an admitted, diagnostic
QUALITY slice of the `audio-runtime` project. The implementation is confined to
`scripts/hermetic-profile/`; run evidence belongs beside this file. It does not
change `Makefile`, `tools/timingate`, existing tests, workflows, coverage
manifests, architecture baselines, C08 recording/replay code, or factory
configuration.

## Existing lane contract

The profiler inventories the same six modules as `make test-hermetic` and keeps
the existing wrapper and timeouts:

| module | directory | command shape | timeout |
| --- | --- | --- | --- |
| `agent-cli` | `agent-cli` | `go run ./cmd/testtimeout --timeout 480s -- go test ...` | 480s target-wide |
| `go-agent-loop` | `go-agent-loop` | `go test ...` | 300s |
| `go-llm-gateway` | `go-llm-gateway` | `go test ...` | 300s |
| `go-audio` | `go-audio` | `go test ...` | 300s |
| `go-device-gateway` | `go-device-gateway` | `go test ...` | 300s |
| `go-agent-runtime` | `go-agent-runtime` | `go test ...` | 300s |

Every measured command uses `CGO_ENABLED=0`, the `nomicrophone` build tag,
explicit `GOMAXPROCS` and `go test -p`, `-count=1`, and the module's unchanged
timeout. `GOCACHE` and `GOMODCACHE` are owned directories below the run
output; cache paths outside the manifest output root are rejected, and the
profiler never clears a shared cache.

`test-budget` is a separate PR-tier inventory. Its 60-second `tools/timingate`
budget is cumulative package terminal time, not lane wall time. C11 reports a
diagnostic reproduction of those semantics and never creates a new mandatory
gate. Package `pass`, `fail`, and `skip` terminals require an `Elapsed` value;
test-level terminals are not package observations. Repeated package
start/terminal cycles are accepted exactly as timingate accepts them, while a
second start before the prior package terminal remains malformed. `Elapsed` is
finite, non-negative, and bounded by Go `time.Duration`; oversized values and
integers fail closed. A failed process, failed package/test, incomplete
inventory, cache marker, malformed JSON, empty stream, or missing raw artifact
makes fresh timing invalid even if a package duration was emitted.

## Public commands

All entry points are standard-library Python and are invoked through the public
script, not imported by the controls:

```text
python3 scripts/hermetic-profile/profile.py inventory \
  --repo "$C11_SOURCE" --output "$C11_EVIDENCE/run" \
  --allow-heavy --quiet-evidence "$C11_QUIET_EVIDENCE"

python3 scripts/hermetic-profile/profile.py warm \
  --manifest "$C11_EVIDENCE/run/manifest.json" \
  --allow-heavy --quiet-evidence "$C11_QUIET_EVIDENCE"

python3 scripts/hermetic-profile/profile.py run \
  --manifest "$C11_EVIDENCE/run/manifest.json" \
  --allow-heavy --quiet-evidence "$C11_QUIET_EVIDENCE"

python3 scripts/hermetic-profile/profile.py run \
  --manifest "$C11_EVIDENCE/run/manifest.json" --cohort "$C11_COHORT" \
  --repeat 2 --allow-heavy --quiet-evidence "$C11_QUIET_EVIDENCE"

python3 scripts/hermetic-profile/profile.py analyze \
  --manifest "$C11_EVIDENCE/run/manifest.json" \
  --output "$C11_EVIDENCE/analysis"
```

The quiet-evidence file is an input artifact and must live below the output
root: below the inventory output for `inventory`, and beside the manifest for
`warm` and `run`. Absolute paths and traversal that resolve outside that root
are rejected before the file is read. This keeps captured quiet-runner
provenance within the manifest's artifact boundary.

`inventory` records the source SHA and dirtiness, runner OS/architecture/CPU,
Go version and effective `go env`, the six `go list -json` package inventories,
flags, cache paths, and command artifacts. `warm` separately records dependency
downloads and `go test -run '^$' -c` test-binary compilation; it does not run
tests. The first `run` is the one uncached full inventory pass. A cohort is at
most five packages and may be invoked at most three times total: the first
successful ranking plus two unchanged repeats. A full lane is single-shot and
is never retried to turn a failure green. Before every measured run command,
the profiler rechecks the source repository's current HEAD and dirty paths
against inventory provenance and records the validation; changes outside the
manifest output root reject the run before the test subprocess starts. Hermetic
analysis requires those per-record validations.

Every child process is started without a shell, with stdin closed, a bounded
deadline, and a new process group where supported. The command record keeps the
exact argv, cwd, environment overrides, exit status, timeout/signal, UTC and
monotonic start/end values, wall interval, raw stdout/stderr paths, byte counts,
and SHA-256 hashes. Go test JSON remains on stdout; diagnostics remain on
stderr. Offline `analyze` only reads retained artifacts.

## Analysis rules

The analyzer parses the same package-terminal event model used by timingate,
ranks package completion durations, retains failures, marks no-test/skip packages separately,
flags cached output, detects overlapping active subtests, and rejects missing or
unexpected package terminals. It rejects unvalidated hermetic source records,
malformed run-record objects, missing or inconsistent command timing metadata,
and no-test markers that contradict the inventory `has_tests` classification.
It reports:

- cold metadata/inventory and warm download/compile time separately;
- package terminal time and ranking, with the existing 60-second PR-tier policy
  referenced from `tools/timingate` rather than reimplemented by offline
  analysis;
- lane wall as `max(monotonic_end) - min(monotonic_start)` across invocations;
- the sum of invocation wall intervals only as a diagnostic, never as lane wall;
- all first-pass/repeat records, variation, raw references, and failure reasons.

Build attribution not directly instrumented by Go is labelled residual/estimate.
The 180-second under-three-minute value is an observation target only. No result
is a whole-project acceptance claim.

## Quiet-runner gate and blocked fallback

Before `inventory`, `warm`, or `run`, the operator must inspect the admitted
factory board and worker-session lease list, host process activity, and load.
The quiet evidence must identify the runner and include, in both `before` and
`after`, `active_work`, a `processes` or `process_activity` observation, and a
`load` observation,
and prove `isolated` or `dedicated` operation. `--allow-heavy` alone is not
permission to consume the shared factory host. If C08 or any other heavy owner
is active, no Go listing, dependency download, warm build, full suite, or timing
run is started. Elapsed time cannot prove quiet and a shared lock cannot control
nonparticipants.

When exclusivity is unavailable within the worker budget, retain the live
before/after evidence and mark `fresh_timing=BLOCKED`. Analyze only immutable
hosted logs whose source/job/command provenance is recorded; unknown runner,
cache, repeat, or wall fields remain unknown. This fallback is an honest
assessment, not a timing pass and not a waiver.

`controls.py` uses synthetic Python JSONL fixtures only, plus a temporary local
Git repository for the source-dirtiness control. It verifies the public entry
point for successful repetition, repeated package terminals, package/process
failures, oversized durations/integers, truncated, malformed, empty, missing,
cached, no-test, and overlapping-subtest streams, plus help/offline no-spawn,
invalid shared-host evidence, and post-inventory source rejection. It does not
invoke Go, download dependencies, build tests, open a live Realtime session, or
claim physical/acoustic proof.
