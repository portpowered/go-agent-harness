# C111 bounded CI-failure characterization

Source revision: `b5f6e422883397f5a3b07a3c80a583f0d8003fd8`.

## Coverage

The previous exact-head Script CI run `34723920469` at `9712ed8b2a7879d08dd620eb0a64a895b7309e24` reported the unchanged coverage-floor failure:

`github.com/portpowered/go-agent-harness/go-agent-runtime/services/session`: expected `80.00%`, actual `76.80%`, delta `-3.20%`.

The current exact-head Script CI rerun `34729269748` at `b5f6e422` passed its coverage gate: `179` registered packages across `7` profiles. The current log also records the C111 package's expected package-local `0.0%` line because the package has no direct tests; that line is not the aggregate floor measurement. A standalone `go test ./services/session -cover` reproduced only that non-authoritative `0.0%` package-local observation and did not change code or coverage configuration. No coverage floor was lowered and no C110 path was absorbed.

## High-rate integration

The previous exact-head integration failure was limited to `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`: it timed out at `47.52s` before the final PCM marker, with rendered PCM `473280`, nonzero PCM `138391` versus expected `174391`, zero queue/drop/overflow/discard counters, `986` callbacks, `9` provider responses, `7` tool results, and a child still running.

The prescribed bounded characterization command was run once against the current source:

```text
GOWORK=off go test ./test/integration -run '^TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst$' -count=1 -timeout=90s -v
```

It passed in `24.991s` total (`12.39s` subtest), with the named subtest reporting `PASS`, exit `0`, and no timeout. The former marker miss did not reproduce. No C111 continuation behavior change is demonstrated, so no C111 repair was made; the prior playback-drain observation remains separate C64 terminal-drain evidence if it recurs.

## Current gate and next action

The current PR #496 head `b5f6e422` has unit, race, integration, coverage, hermetic, WebMCP Chrome, macOS audio software, and Windows portable software checks green. Static fails only at `make wire-check` and `make architecture-size-check` because `go-agent-runtime/services/sessioncontinuation/wire/wire_gen.go` is not yet registered; C79/work-task-114 exclusively owns that shared registry and architecture baseline and has not released them. The isolated worktree remains clean and the shared paths remain untouched.

After C79's reviewed guarded merge explicitly releases those files, fetch and merge the then-current `origin/main` without reset, apply only the demonstrated C111 Wire registration and any directly demonstrated downward baseline delta, rerun bounded gates, update PR #496, and submit that changed head to Script CI without polling. Until then, retain this same task and do not edit the shared files.

## Fresh current-main revalidation

After the no-reset merge of freshly fetched `origin/main` `071b0abfd67501db61e3c1929971c6dd6e77eb62`, source revision `eea5834ebe11d4bdb1a88d6bb369dcb2f7d75c25` passed the same bounded characterization:

```text
GOWORK=off go test ./test/integration -run '^TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst$' -count=1 -timeout=90s -v
```

The command exited `0` in `22.211s`; the named `provider_burst` subtest passed in `12.44s`, with the strict final PCM marker reached. The prior timeout did not reproduce, so no C111-owned repair was made; retain the separate C64 terminal-drain observation if it recurs. Post-merge public replay and the four accumulated C111 regressions also exited `0` with bounded, reaped process groups. Script CI remains the next external gate and is not claimed green.

## Latest bounded executor rerun

At candidate head `6a41574b4f8e0eb090a9d515839ed9d15f10ec95`, the same
causal `test46/provider_burst` command passed in `22.586s` total, with the
named subtest passing in `12.38s` and the strict final PCM marker reached. The
accumulated `COUNT=1 bash scripts/test-session-ci-regressions.sh all` command
also passed its normal, coverage, and race lanes, including all `20/20`
high-rate trials and the four named C111 continuation regressions. No source
repair was demonstrated or made; the worktree remains clean after the test
runs. These are executor checks only and do not claim Script CI, review, merge,
or acceptance.

## Final integrated-main rerun

After the no-reset merge `44747db8fca0a1f5e11ce9722b709976316b943a` of the
fresh `origin/main` `bd6a1289218d1bef1a3af36e64e9d4496062416f`, the same causal
command passed in `24.059s` total, with the named subtest passing in `12.55s`
and the strict final PCM marker reached. The accumulated regression command
passed again in normal, coverage, and race lanes, including `20/20` high-rate
trials and the four named C111 continuation regressions. The integrated main
change is outside C111 ownership; no C111 source repair was demonstrated or
made. These are executor checks only and do not claim Script CI, review, merge,
or acceptance.
