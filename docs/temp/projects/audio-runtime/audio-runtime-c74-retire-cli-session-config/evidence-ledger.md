# C74 evidence ledger

Evidence is recorded only for this admitted task slice. Each checkpoint names
the exact source revision, focused causal tests, public-consumer result, and
any shared prerequisite still owned by another task. A passing local test is
not reported as script-CI green and no physical-device or acoustic claim is
made on the Darwin validator.

## Disjoint implementation checkpoint

- Admission remains the sole admitted `audio-runtime/audio-runtime-v1` task on
  `codex/audio-runtime-c74-retire-cli-session-config`; startup integration is
  `8bdafc7f947a3a2c9856220abdc539437035bd21`, the admitted accepted baseline is
  `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`, and freshly fetched `origin/main`
  is `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`. The isolated worktree
  integrates that disjoint mainline checkpoint in merge revision
  `5c7e0ea2076e25982f57c57deea829117dad0f28`; all required ancestry checks
  pass. The worktree is clean before the checkpoint except for the owned
  implementation, tests, manifests and evidence.
- The accepted-main `session_options.go` census is exactly `930` lines. The
  candidate is `619` lines, retiring `311` physical production lines; the
  candidate remains below the `680` limit and above the `250` retirement floor.
- The public `sessionconfig` contract owns typed request/result/error values;
  private service policy owns capture/timing, runtime selection/conflict
  attribution, provider/model/capability resolution, URL normalization and
  deep cloning. The CLI keeps only request/config adapters and concrete
  provider/dialer/inferencer construction. No public-service import scan found
  CLI, concrete provider, HTTP, exec, init or environment access.
- Normal/race focused service policy passed (`14` selected normal tests and
  `36` selected race repetitions); the full package suite passed `26` tests in
  three packages. The external `GOWORK=off` module compiled cleanly. The
  bounded consumer cases passed: default `ws`, explicit `webrtc`, recorded
  credential-free replay, and expected exit-1 invalid-model/invalid-transport
  controls. CLI focused regressions passed `343` normal tests, `108` race
  tests, and the two remote-device preflight tests.
- The literal oracle is table-driven and independent of exported option
  constants: it freezes omission/explicit transport, equal/conflicting
  signaling aliases, prerequisite fields and typed causes, capture/timing
  messages, injected replay admission, provider/model defaults and explicit
  empty rejection, capabilities, the OpenAI credential sentinel, URL behavior,
  nil error values and turn-detection ownership. Both mutation controls run a
  positive row before the causal negative control and report rejection.
- Package floors pass independently: public contract `88.2%`, private service
  `90.9%`, generated Wire `100%`; `coverage-registration` reports `179`
  workspace packages across six modules. Pinned `make vet`, `make staticcheck`
  and `make lint` pass with zero issues. Changed-base coverage was rerun after
  adding the missing package coverage branches and completed without a floor
  violation or make failure.
- The read-only architecture gate now reports only five expected shared or
  generated-file findings: the C74 line-count drift and three stale complexity
  entries in the shared architecture ledger, plus the unregistered
  `go-agent-runtime/services/sessionconfig/wire/wire_gen.go`. C57 retains
  `scripts/wire-packages.txt`; C61 retains
  `docs/architecture/architecture-size-baseline.json`. No shared file was
  edited. The read-only Wire gate reports exactly that one unregistered path.
- This checkpoint is executor evidence only. It does not claim script CI,
  independent review, guarded merge, immutable executable validation,
  physical/acoustic proof or project completion. After commit/push, the exact
  next action is to retain the same task until the two shared leases release,
  then mediate only the demonstrated Wire registration and downward/stale C74
  architecture entries and rerun the final scoped gates before submitting the
  same PR head to script CI.

## Reverification after the first script-CI rejection

- On `2026-09-12T06:32:15Z`, the exact pushed source revision remained
  `308466151f930f3b1ca1be74023d4f2ddd37c2dc`, with fetched `origin/main`
  `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`; the worktree was clean and the
  startup, planning-main and current-main ancestry checks remained true.
- The focused C74 verifier passed again, including the independent literal
  matrix, the two fail-closed mutation controls, normal/race sessionconfig
  tests, the GOWORK=off public consumer, CLI parity, and the `930 -> 619`
  (`311` retired) line census. The exact accumulated session regression
  command `COUNT=1 bash scripts/test-session-ci-regressions.sh` passed in all
  three modes: `normal`, `coverage`, and `race`. Each retained its expected
  negative replay diagnostics, 20 high-rate trials, simulated-device checks,
  and composed OpenAI tool-result checks; no source or assertion was changed.
- PR `#467` remains open at the same head with no review or comment findings.
  The full terminal run `34674915937` was read from saved job logs: static
  failed only on the unregistered
  `go-agent-runtime/services/sessionconfig/wire/wire_gen.go`, the C74
  `session_options.go` `619 > 930` downward drift, and its three stale
  complexity entries; integration failed only on the peer-owned test46
  high-rate loss of `6,400` samples; hermetic failed only on the peer-owned
  WebRTC camera frame timeout. Those peer failures are not C74 repairs.
- C57 `work-task-26` and C61 `work-task-34` remain terminal `FAILED` rows with
  retained ownership of `scripts/wire-packages.txt` and
  `docs/architecture/architecture-size-baseline.json`, respectively. No
  shared-file mutation is authorized at this checkpoint. The candidate is
  clean, pushed and already represented by PR `#467`; it must remain with the
  same task until those exact leases are released, then receive only its
  demonstrated Wire registration and downward/deleted C74 ledger changes
  before the scoped gates and a changed-head script-CI handoff are repeated.
