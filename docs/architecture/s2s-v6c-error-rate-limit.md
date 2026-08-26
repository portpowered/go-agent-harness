# Proof — s2s-v6c-error-rate-limit

Date: 2026-08-25. Lane: `s2s-v6c-error-rate-limit`. Tier: T1 hermetic only —
every claim below was produced offline over committed session fixtures with
the production CLI. No credentials, no network, and **no live-provider or
retry/backoff behavior claim of any kind**: the vertical proves provider
throttling *classifies* distinctly; it says nothing about how a live session
should react to throttling.

## Claim

A provider rate-limit response surfaces as a typed, observable terminal
classification distinct from authentication failures and generic rejections:

- the committed capture carries wire fields `error.type=rate_limit_error` plus
  `error.code=rate_limit_exceeded`;
- replaying it into the registered case exits **0** with JSONL
  `"pass":true` and `"terminal_reason":"error:rate_limited"`;
- replaying the comparable invalid_api_key control into the unchanged
  expectation exits non-zero with `"pass":false` and observed
  `error:authentication`;
- replaying the comparable invalid_request_error/bad_request control into the
  unchanged expectation exits non-zero with `"pass":false` and observed
  `error:provider_rejected`;
- in both failed outcomes the machine-readable outcome reports
  `expected:"error:rate_limited"` beside the observed actual — **the terminal
  reason, not human-readable error text, is the oracle**;
- suite-prefix selection (`--scenario s2s-v6c-error-rate-limit`) resolves to
  the same single registered case.

This boundary is explicit: v6a proves the auth classification
(`error:authentication`), v6b proves clean-disconnect detection, v6d proves the
malformed/parse classification (`error:invalid_request`); v6c adds the throttle
member so no two failure families collapse into one observable verdict.

## Landed artifacts

| artifact | path |
|---|---|
| scenario registration | `go-agent-loop/pkg/probe/scenario_error_rate_limit.go` (case `s2s-v6c-error-rate-limit-throttled`, expectation `error:rate_limited` over send-text+close steps) |
| positive capture | `go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v6c-error-rate-limit-throttled.session.json` |
| auth negative control | `go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v6c-error-rate-limit-negative-auth.session.json` |
| invalid-request negative control | `go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v6c-error-rate-limit-negative-invalid-request.session.json` |
| integration proof | `agent-cli/test/integration/s2s_v6c_error_rate_limit_test.go` |
| fixture corpus gate | `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go` (exact count recomputed to 35 on this head) |

## Reproduction

From the repository root:

```bash
go run ./agent-cli/cmd/agent probe run \
  --scenario s2s-v6c-error-rate-limit-throttled \
  --replay go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v6c-error-rate-limit-throttled.session.json \
  --json
```

Verified by execution on the lane head — exit code `0`, one JSONL result line
on stdout:

```json
{"name":"s2s-v6c-error-rate-limit-throttled","pass":true,"expectations":[{"index":0,"kind":"terminal-reason","passed":true}],"ticks":1,"frames":2,"terminal_reason":"error:rate_limited"}
```

and the run summary on stderr:

```json
{"total":1,"passed":1,"failed":0,"status":"pass"}
```

Negative controls (same argv shape, `--replay` pointed at each control
capture): each exits `1` with `"pass":false`,
`"terminal_reason":"error:authentication"` /
`"terminal_reason":"error:provider_rejected"`, and an outcome carrying
`expected:"\"error:rate_limited\""` beside
`actual:"\"error:authentication\""` /
`actual:"\"error:provider_rejected\""`. Both controls also replay cleanly
inside the deadline when their own classification is asserted, so the failing
runs fail on the classification mismatch alone — never on a launch or
fixture-read error.

The CI-resident version of this proof is
`TestV6CErrorRateLimitThrottledExitsZeroOffline` plus its three sibling tests
in `agent-cli/test/integration/s2s_v6c_error_rate_limit_test.go`, which drive
the production root command with real argv under a hard two-second parent
deadline and assert the exit codes, JSONL fields, and expected-vs-actual
detail above.
