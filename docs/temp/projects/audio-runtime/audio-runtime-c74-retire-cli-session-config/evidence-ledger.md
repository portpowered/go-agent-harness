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

## Shared static-gate repair checkpoint

- Direct admission verification remains `admitted` for the sole
  `audio-runtime/audio-runtime-v1` task `work-task-98`. The isolated branch is
  `codex/audio-runtime-c74-retire-cli-session-config`; fetched `origin/main` is
  `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`, and startup, planning-main,
  accepted-main and current-main ancestry checks pass. C57 `work-task-26` and
  C61 `work-task-34` are terminal `FAILED`, releasing the two exact shared
  leases; no peer edits or host-checkout mutation were made.
- The complete latest CI rejection was read from run `34678513215`, head
  `5d04b1ff1fd8d1ffaecaed81d68e022149e6e8eb`. The only failed job was static:
  Wire reported the unregistered `sessionconfig/wire/wire_gen.go`, and the
  architecture lane reported C74's `619 > 930` file drift, stale
  `resolveOpenAIRealtimeSessionConfig` cognitive/cyclomatic entries, stale
  `resolveSessionRuntimeSelection` cognitive entry, and the same generated
  registration finding. Unit, race, integration, coverage, hermetic, WebMCP,
  macOS audio release and Windows portable software passed. PR `#467` has no
  review findings or review comments.
- The bounded repair changes only the demonstrated registries: registers
  `go-agent-runtime/services/sessionconfig/wire` in
  `scripts/wire-packages.txt`, registers its exact generated file in
  `docs/architecture/architecture-policy.json`, lowers the C74 file baseline
  `930 -> 619`, and deletes only the three stale C74 complexity entries from
  `docs/architecture/architecture-size-baseline.json`. No threshold was
  raised, no peer entry was absorbed, and no runtime source was changed.
- Post-repair checks pass: `make wire-check`; architecture/size at `189`
  packages, `1,902` files and `28,028` functions; coverage registration at
  `179` packages across six modules; vet; pinned staticcheck `2026.1`; pinned
  golangci-lint `2.9.0` with zero issues; sessionconfig normal/race `26/26`;
  CLI focused normal/race `343/108`; the complete verifier including both
  mutation controls; the full C74 public runner matrix; and accumulated
  session regressions in normal, coverage and race modes with `COUNT=1`.
  `make fmt`, JSON validation, diff-check, and the 930/619/311 line census
  also pass. The repository's bounded changed-base coverage target completes
  successfully with exit `0` and no floor violation.
- This remains executor handoff evidence only. After changed-base coverage
  completes, commit and push the same PR `#467`, update its body with this
  repair map, and return `ACCEPTED` to the script-owned CI gate without
  polling. Do not claim CI green, independent review, guarded merge,
  immutable-artifact vertical acceptance, physical/acoustic proof or project
  completion; retain this task through `CONTINUE` for any exact rejection.

## Review-132 repair and mutation checkpoint

- On `2026-09-12`, the admitted `work-task-98` candidate repaired every
  actionable review finding from review row `work-review-132`. The source
  revision is `6efc2cf8dd5906d911b936a1981a24639b4ed2c2`. The service now
  honors `ProviderProvided` and `ModelProvided`, so an explicit empty provider
  cannot fall through to persisted defaults and an explicit empty Grok model
  cannot fall through to the configured model. Literal Grok rows now assert
  copied defaults, explicit overrides, provider mismatch, explicit empty
  provider, explicit empty model and omitted model behavior. The CLI edge
  adapter and its provider precedence regression cover the same explicit-empty
  provider rule.
- The transport consumer control now supplies WebRTC plus conflicting
  `Signaling`/`SignalingEndpoint` aliases and asserts the typed
  `signaling`, `signaling-endpoint` field pair and
  `ErrSessionRuntimeSelectionConflict`; it no longer uses `quic` as the alias
  conflict control. `run.py --case all` passed all seven bounded cases,
  including the expected invalid-model and conflicting-alias rejections.
- `verify.py --mode mutation-accept-conflicting-transports` passed: the
  positive `alias_conflict` row passed, an isolated temporary mutation that
  disabled alias-conflict rejection failed that same row with `error = <nil>`,
  and the real worktree was clean after detached-worktree cleanup.
  `verify.py --mode mutation-alias-turn-detection` passed: the positive clone
  row passed, an isolated temporary mutation returning the caller's turn
  detection pointer failed with `turn detection was not deeply cloned`, and
  the real worktree was clean after cleanup. Neither mutation persisted.
- `verify.py --mode frozen-option-matrix` passed: accepted-main baseline
  `930`, candidate `619`, retired `311`, independent literal matrix green,
  GOWORK=off consumer build/replay/default transport/explicit WebRTC checks
  green, and both causal negative controls rejected. `verify.py --mode parity`
  passed sessionconfig normal and race tests plus focused CLI regressions.
  The candidate tree passes `git diff --check` and is clean. This is executor
  evidence only; it does not claim script CI, independent review, guarded
  merge, immutable validation, physical/acoustic proof or project completion.
  The next action is push this same revision, update PR `#467`, and submit the
  changed head to script-owned CI without polling.
