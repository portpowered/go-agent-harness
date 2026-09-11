# C63 terminal-outcome retirement evidence

Task: `audio-runtime-c63-retire-cli-terminal-outcome`.

Project admission is `audio-runtime/audio-runtime-v1`; no second project or
acceptance waiver was used. The admitted task verifier returned:

```text
{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c63-retire-cli-terminal-outcome"}
```

The isolated worktree is
`/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c63-retire-cli-terminal-outcome`
on branch `codex/audio-runtime-c63-retire-cli-terminal-outcome`. The candidate
is clean at `ca241bf50ecd0aba24633c5f3dd00af2b0fbe40e`, five commits ahead of
freshly fetched `origin/main` `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`.
Startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, planning main,
and baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` are ancestors.

## Ownership and retirement

Only the admitted legacy file/test, new `go-agent-runtime/services/terminaloutcome/**`,
its coverage manifests, and this evidence directory changed. Existing
agentruntime caller files are byte-identical; the excluded caller diff command
returned no output. The accepted production baseline is 576 physical lines;
the candidate adapter is 125 lines, a direct 451-line reduction. The adapter
only forwards context, lifecycle evidence, publication, and the legacy
duration sentinel. State, synchronization, precedence, normalization, bounded
error traversal, rendering, and publication policy are private to the runtime
service.

The shared files remain untouched:

```text
scripts/wire-packages.txt                         unchanged
docs/architecture/architecture-size-baseline.json unchanged
```

C57's latest board/meta result released its Wire-registry lease, while the top
implementation handoff still records the older C57 lock. C61 still owns the
architecture baseline. C63 therefore does not edit either shared file until a
synchronized handoff or owner-mediated canonical lease amendment authorizes
the exact downward-only changes.

## Final-head hashes

```text
952d5a2f302552d9cd585ef96780d119c2d1ef23c1a815518239b64089f79e88  agent-cli/internal/services/internal/agentruntime/session_terminal_outcome.go
7a22e03b6d305838f162695c0bf72b974fca7a3c5477246bba6e29da265b83d8  agent-cli/internal/services/internal/agentruntime/session_terminal_outcome_test.go
453a82f4134fcfe61d82df649de8ee6a645514a5ad930005b2708ec896a57bac  go-agent-runtime/services/terminaloutcome/contract.go
c329c84f7a7ad478a3fe58618f3f448290c1fa9fae287c10fce2e9941c2514bc  go-agent-runtime/services/terminaloutcome/internal/service/errors.go
5a6791f20309014083469164c0844b4b82a03dc30c67801bc5dcab6d8f0482a1  go-agent-runtime/services/terminaloutcome/internal/service/observe.go
2f64a5cfa190960b5daa9bd4167f768b80a7e55e27b63a89496d715f4e976ca4  go-agent-runtime/services/terminaloutcome/internal/service/reconcile.go
dd1130a3327731507fca2a7fc5d75dec448dd321583df677adffdb9ff819f7cd  go-agent-runtime/services/terminaloutcome/internal/service/render.go
5fe0826bc801ca30a4ddbda142ee2e4d96ba2e80b54f5d68338e1cecb7645c8e  go-agent-runtime/services/terminaloutcome/internal/service/service.go
58e722b29f192cf3711d84a00ff074c3c6d2dfde5545efc834be0cb395b0ac31  go-agent-runtime/services/terminaloutcome/internal/service/state.go
7cd19268060da97f2f801fdee0426a68d23b7b7ed8114122564e66570ab1d566  go-agent-runtime/services/terminaloutcome/wire/providers.go
0d3c01832b2aa7b29b0858a9beaed067a86f175e870df35c2e20c50cd5fc91f4  go-agent-runtime/services/terminaloutcome/wire/wire_gen.go
96c3536510995f6393829d0a3ee8f2c1a1b9b73f2310a0da09348e499d4d70af  coverage-manifest/go-agent-runtime/services/terminaloutcome/package.json
dedf32aee0a289f0ecc386cf32f7db88f92186660157a8fd5c2df4e67d73f7e4  coverage-manifest/go-agent-runtime/services/terminaloutcome/internal/service/package.json
4ec250fa9a2651f49bec012d97d0eaac705698c7dc453d0584630b9e72420a34  coverage-manifest/go-agent-runtime/services/terminaloutcome/wire/package.json
```

## Focused gates

Final-head results:

```text
go test ./go-agent-runtime/services/terminaloutcome/... -count=1       PASS (50 tests, 3 packages)
go test -race ./go-agent-runtime/services/terminaloutcome/... ...      PASS (141 selected runs, 3 packages)
go test ./agent-cli/internal/services/internal/agentruntime ...         PASS (308 tests)
make coverage-registration                                              PASS (179 workspace packages)
make vet                                                               PASS
make lint                                                              PASS (pinned golangci-lint 2.9.0)
make staticcheck                                                       PASS (pinned staticcheck 2026.1)
private service coverage                                                PASS (96.4%, floor 95.0%)
make coverage-changed COVERAGE_BASE=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06 PASS (179 registered packages, 7 profiles)
go generate ./services/terminaloutcome/wire (GOWORK=off)                PASS; unchanged output
forbidden CLI/device import scan                                           PASS; no matches
caller diff excluding the two owned legacy files                       PASS; no output
git diff --check                                                        PASS
```

The tests cover ordered fatal/replay/cancellation/observed/duration/fallback
precedence, all supported stream payload classes and empty values, artifact
validity, replay/duration markers, nil and typed-nil behavior, joined/wrapped/
cyclic/over-depth errors, exact terminal/newline/replay bytes, short writes,
typed writer identity, bounded field/error retention, and concurrent exactly-
once publication. Negative controls assert the second-publication sentinel,
mixed independent cancellation as fatal, fail-closed incomplete/cyclic
evidence, and no unbounded render retention.

## Separate consumer

The temporary module under `consumer/` imports the public terminaloutcome
contract, its dedicated Wire package, and shared message types only; it has no
CLI, flag, device, or hidden initialization dependency.

```text
d3f175c1965e7926b4fe0f0705100018c9eab3945e52fad7b5028584ef6ca92a  consumer/go.mod
7806477cd3f52b90dfb17535a8b0d2646c71d488c7b78d1d0c35dff5d9333acf  consumer/main.go
```

`GOWORK=off go test ./... -count=1` passed, and `GOWORK=off go run .`
executed the contract and asserted failure behavior. Exact output:

```text

[session terminal: classification=consumer terminal_reason=provider_authored_completion terminal_provenance=provider output_state=complete]
```

## Shared-gate handoff status

The unmodified shared gates report only lease-dependent findings:

```text
make wire-check
  FAIL: unregistered=['go-agent-runtime/services/terminaloutcome/wire/wire_gen.go']
make architecture-check
  FAIL: generated-file-spoof .../services/terminaloutcome/wire/wire_gen.go
make size-check
  FAIL: three baseline-stale entries for the retired legacy file
```

No limit was raised and no peer finding was absorbed. Exact next action: retain
this pushed checkpoint, wait for synchronized handoff/owner release, add only
the C63 Wire registry entry and demonstrated downward/deleted C63 baseline
entries through the current owners, rerun shared gates, then submit the changed
head to Script CI. Independent review, guarded merge, and the fresh immutable
vertical probe remain required and are not claimed here.
