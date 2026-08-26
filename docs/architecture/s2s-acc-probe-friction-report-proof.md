# s2s acceptance-probe friction report — deterministic mixed-corpus proof

Status: **proven in-repo** (2026-08-26) by a hermetic CLI test over committed
JSONL artifacts; no network or credentials are required.

## What is proven

`TestProbeReportMixedCorpusIncludesExpectationAndTopFrictionRollups`
(`agent-cli/internal/cli/probe_test.go`) drives the real root command with the
committed fixture `agent-cli/internal/cli/testdata/probe-reports/mixed.jsonl`.
The fixture contains a passing scenario, two expectation-miss failures, and a
session marked `terminal_reason:"stuck"`, followed by the existing derived
`RunSummary` record.

The test decodes the JSON report and asserts:

1. **Health rollup.** Four result records produce one pass, three failures, and
   one stuck failure; the stuck record remains in the failure total.
2. **Expectation misses.** `transcript-contains` misses twice and
   `frame-count` misses once, with the representative scenario name retained
   for each kind.
3. **Ranked frictions.** The most frequent category is the two-count
   `expectation/transcript-contains` friction. Ties are deterministic and the
   report includes distinct stuck, error-class, and terminal-reason categories
   with representative scenario names.
4. **Human summary.** The `--summary` output names the expectation and top
   friction categories, their counts, and representative scenarios.

## Re-running offline

```text
cd go-agent-loop && go test ./pkg/probe -run TestAggregateFrictionReport -count=1
cd ../agent-cli && go test ./internal/cli -run TestProbeReport -count=1
```

The report reader consumes the existing `ScenarioResult` and `RunSummary` JSONL
records unchanged. It ignores only the derived summary record; no runner output
format or stuck-detection behavior is introduced by this lane.
