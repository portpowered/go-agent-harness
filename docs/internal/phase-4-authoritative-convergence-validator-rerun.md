# Phase 4 Authoritative Convergence Validator Rerun

This report reruns the Phase 4 public API contract convergence review from the
current authoritative baseline. It is intentionally incremental while the PRD
stories are being completed: this section establishes the branch, commit, and
evidence disposition that later row verdict sections must use.

## Authoritative Review Baseline

Branch under review:
`phase-4-authoritative-convergence-validator-rerun`.

Commit under review for story 001:
`8633cb6768623fd7342d7a19d9dbf2e260f4e95f`.

That commit is the current `origin/main` head after PR `#40`, and this work
branch was fast-forwarded to it before this report was written. The head
includes the authoritative Phase 4 baseline from
`phase-4-authoritative-baseline-sync` because
`origin/phase-4-authoritative-baseline-sync` is an ancestor of this head.

The head also includes the typed-terminal reconciliation decision from
`phase-4-typed-terminal-authoritative-reconciliation` because
`origin/phase-4-typed-terminal-authoritative-reconciliation` is an ancestor of
this head.

Reviewer commands:

```sh
git rev-parse HEAD origin/main origin/phase-4-authoritative-baseline-sync origin/phase-4-typed-terminal-authoritative-reconciliation
git merge-base --is-ancestor origin/phase-4-authoritative-baseline-sync HEAD
git merge-base --is-ancestor origin/phase-4-typed-terminal-authoritative-reconciliation HEAD
git log --oneline --grep='Merge pull request #3[7-9]\|Merge pull request #40' origin/main
```

Expected result:

- `HEAD` and `origin/main` resolve to
  `8633cb6768623fd7342d7a19d9dbf2e260f4e95f` for this story-001 evidence.
- Both `merge-base --is-ancestor` commands exit `0`.
- The log includes PR `#37` for
  `phase-4-typed-errors-stream-terminal-contract`, PR `#38` for
  `phase-4-api-contract-convergence-validator`, PR `#39` for
  `phase-4-authoritative-baseline-sync`, and PR `#40` for
  `phase-4-typed-terminal-authoritative-reconciliation`.

## Consumed Baseline Decision

The consumed authoritative baseline decision is:

- Use `origin/main` at
  `6e785952affad9cc5d07458c84f9a45b755c72c0` or a descendant that preserves
  that commit in ancestry as the Phase 4 baseline after batch 017.
- The current review head is such a descendant.
- Older local snapshots and pre-merge factory branches are not authoritative
  when they omit, predate, or are superseded by landed remote evidence.
- The baseline sync does not close `P4-API-01` through `P4-API-07` or
  `P4-GATE-01`; it only publishes branch-status and preservation evidence for
  future validator work.

Primary source:
`docs/internal/phase-4-authoritative-baseline-sync.md`.

## Consumed Typed-Terminal Reconciliation Decision

The consumed typed-terminal reconciliation decision is:

- Disposition: `landed`.
- The standalone
  `phase-4-typed-errors-stream-terminal-contract` branch is superseded as a
  planning base because it was landed through PR `#37` and later preserved by
  PRs `#39` and `#40`.
- Future Phase 4 validation and repair work should use `origin/main` or a
  descendant, not the old standalone typed-terminal branch head.
- The typed-terminal reconciliation contributes representative evidence for
  typed errors, stream terminal fields, replay/cancellation/partial-output
  outcomes, CLI/session terminal surfaces, docs alignment, and quality gates,
  but it explicitly does not close whole-Phase-4 `P4-GATE-01`.

Primary source:
`docs/internal/phase-4-typed-terminal-authoritative-reconciliation.md`.

## Prior Evidence Reconciliation

This rerun treats prior evidence as follows:

| Prior evidence source | Current disposition for this rerun |
| --- | --- |
| `phase-4-api-contract-convergence-validator` | Advisory unless matched against this authoritative head. Its previously risky stale-branch status was superseded when PR `#38` landed on `origin/main`, and its durable artifact remains `docs/internal/phase-4-api-contract-validator.md`. Later row verdicts must reconcile that artifact against the current head instead of copying its conclusions blindly. |
| Batch 017 evidence | Authoritative when it is present in current ancestry. PRs `#33` through `#36` landed the repair validator, audit reconciliation, provider capability/local validation contract, and dependency/result/context/lifecycle contract evidence. This head preserves those merges through `origin/main`. |
| `phase-4-typed-errors-stream-terminal-contract` | Advisory as a standalone branch head. Its implementation evidence landed through PR `#37`, then the typed-terminal authoritative reconciliation landed through PR `#40`; use current-head docs/tests instead of the old branch as a base. |
| `origin/main` | Authoritative for this rerun at `8633cb6768623fd7342d7a19d9dbf2e260f4e95f`, because it is the fetched remote head containing PRs `#33` through `#40`. |

Stale branch evidence is advisory only unless the same claim is reproducible on
the current authoritative baseline through public files, exported declarations,
deterministic tests, reviewer commands, or landed docs. CI success alone is not
row closure evidence.

## Row Verdict and Closure Evidence Standard

Every row finding in this rerun must use the same reviewer-grade shape:

- Checklist row: the row id and direct checklist text or a direct citation to
  the checklist source.
- Verdict: exactly one of `pass`, `fail`, or `uncertain`.
- Closure decision: exactly one of `may close` or `remains open`.
- Public evidence: credential-free evidence that a reviewer can inspect or
  rerun from the authoritative head.
- Affected declarations and outcomes: exported declarations, serialized fields,
  returned errors, emitted events, CLI/API behavior, docs, examples, or other
  public user-visible outcomes affected by the row.
- Docs/tests/examples alignment: whether public docs, deterministic tests, and
  examples agree with the claimed behavior, are missing, or conflict.
- Reviewer commands: credential-free commands and the specific behavior or
  artifact each command proves.
- Exact next work: required for every non-pass row and limited to future
  implementation-ready repair or cleanup work.

A `pass` requires public, credential-free evidence from the current
authoritative head. Acceptable pass evidence includes exported Go declarations,
returned Go errors, `errors.Is` or `errors.As` behavior, emitted stream or
session events, serialized payload fields, CLI-visible output, public docs,
examples where present, and deterministic tests that use fakes or fixtures.
Successful CI, implementation intent, private helper behavior, undocumented
internals, or unreconciled stale branch conclusions are not sufficient row
closure evidence.

A `fail` finding must name the observable public evidence that proves the row
does not meet the checklist requirement, such as a missing exported contract,
wrong error/result behavior, missing emitted field, contradictory public docs,
or deterministic test evidence of the wrong behavior. An `uncertain` finding
must name the evidence gap that prevents closure, such as missing public docs,
missing deterministic tests, stale prior evidence that no longer maps cleanly to
the current head, conflicting public artifacts, or evidence that requires live
provider credentials or private local state.

Rows may close only when the row verdict is `pass` and the closure rationale
cites public evidence and reviewer commands from the authoritative head. Rows
remain open when the verdict is `fail` or `uncertain`, when the only evidence is
CI status, private implementation detail, undocumented behavior, non-public
state, or stale branch evidence that has not been reconciled against the current
head.

This validator must not implement feature repairs while producing row verdicts.
For non-pass rows, it must describe future work as a concrete repair or cleanup
task with observable expected behavior and proof requirements, not broad
validator-side investigation.

## Public API Contract Row Verdicts

This section completes story
`phase-4-authoritative-convergence-validator-rerun-003` by evaluating
`P4-API-01` through `P4-API-07` against the current authoritative head. The row
texts are cited from `docs/internal/phase-4-api-contract-validator.md`, and the
current row evidence is reconciled against
`docs/architecture/contract-gap-audit.md`,
`docs/internal/phase-4-typed-terminal-authoritative-reconciliation.md`, public
declarations, deterministic tests, and current docs. These verdicts do not
implement repairs.

### P4-API-01 - Context, Cancellation, And Timeout Ownership

- Checklist row: public APIs that can block or perform provider work expose
  caller-controlled context, cancellation, and timeout behavior clearly enough
  for consumers to own request lifetime.
- Verdict: `fail`.
- Closure decision: `remains open`.
- Public evidence: the current audit marks the row `fail`; constructor and
  runtime ownership are narrowed, but prompt/config/filesystem loading,
  session/config/dialer side effects, complete timeout/cancellation ownership,
  and stable dependency/result errors remain open. Public evidence includes
  `agentloop.New`, `WithInferencer`, `WithSessionInferencer`,
  `WithToolExecutor`, `WithToolExecutionDisabled`,
  `messages.Inferencer`, `messages.SessionInferencer`, `gateway.NewGateway`,
  `gateway.NewSessionGateway`, `inference.NewGatewayInferencer`,
  `inference.NewSessionGatewayInferencer`, `agent.ProviderFactory`,
  `agent.Executor`, and `services.RunSession`.
- Docs/tests/examples alignment: deterministic tests cover explicit
  tool-execution constructor decisions, provider HTTP runtime ownership,
  session runtime planning, and gateway-to-loop adapters, but public docs and
  tests do not yet prove the full timeout/cancellation and hidden-side-effect
  contract across all blocking/provider surfaces.
- Reviewer commands: run `make typecheck` to prove the workspace still
  typechecks with the current public contract surfaces; run
  `(cd go-agent-loop && go test ./pkg/agentloop)` to prove the loop tests that
  cover explicit tool-execution constructor decisions and runtime ownership;
  run `(cd agent-cli && go test ./internal/agent ./internal/services)` to
  prove the CLI composition tests that exercise the session-run wiring
  boundaries.
- Exact next work: split `agent.Executor.loadSystemPrompt` into pure prompt
  assembly plus injected filesystem/config/system-info/skills loaders; document
  whether `services.RunSession` is an internal composition contract or only CLI
  behavior; expose missing live dialers, config files, replay captures, and
  provider construction failures through stable dependency/result errors.

### P4-API-02 - Typed Errors And Stream Failure Contracts

- Checklist row: public gateway, provider, replay, validation, and cancellation
  failures preserve typed or structured classifications so callers can branch
  with `errors.Is`, `errors.As`, or documented fields instead of parsing
  strings.
- Verdict: `fail`.
- Closure decision: `remains open`.
- Public evidence: representative typed-terminal repairs landed on the current
  head, including `messages.ErrorValue` terminal fields, gateway/provider error
  classes, provider classification strings, direct stream normalization, replay
  mismatch/incomplete outcomes, cancellation evidence, and CLI/session tests.
  The current audit still keeps the broader row open because loop participants,
  stream adapters, interaction-event bridging, provider-wide parity, and CLI
  session command paths still include caller-visible string-only or
  phase-prefixed error paths.
- Affected declarations and outcomes: `messages.ErrorValue`,
  `messages.NewErrorValue`, `messages.NewErrorValueWithDetails`,
  `messages.StreamTypeError`, `messages.Session`, `messages.SessionInferencer`,
  `gateway.GatewayError`, `gateway.ProviderHTTPStatusError`,
  `gateway.InteractionError`, `gateway.InteractionEventError`,
  `gateway.InteractionCancellation`, `testing.NewSessionReplayer`, and
  `testing.WithReplayContext`.
- Docs/tests/examples alignment: tests cover OpenAI Realtime detail
  preservation, PNIG provider/timeout/cancellation events, replay divergence,
  omitted outbound events, cancellation-stopped replay, record/replay
  cancellation, and selected `ERROR` stream handling. The docs and tests do not
  yet prove provider-wide and parser-wide parity for every adapter, helper
  entrypoint, and stream failure path.
- Reviewer commands: run
  `(cd go-llm-gateway && go test ./pkg/gateway ./pkg/testing ./pkg/providers/openai ./pkg/providers/anthropic ./pkg/providers/gemini ./pkg/providers/grok)`
  to prove the gateway/provider/tests that preserve typed terminal fields and
  error classification; run
  `(cd go-agent-loop && go test ./pkg/participants ./pkg/subsystems ./test/functional)`
  to prove the loop participants, subsystems, and functional stream adapters
  still surface the representative failure and cancellation paths; run
  `(cd agent-cli && go test ./internal/services)` to prove the CLI session
  command paths that emit the row-relevant error classes.
- Exact next work: define shared caller-actionable stream error classes and
  convert loop/provider/interaction bridge emitters away from string-only
  `NewErrorValue(err.Error())`; define typed CLI/session categories for
  validation, provider-connect, replay-divergence, provider-runtime,
  cancellation, and capture-persist failures; document which failures are
  returned as Go errors versus emitted in-band as stream/session events.

### P4-API-03 - Result, Failure Signal, And Lifecycle Contracts

- Checklist row: public result values and stream events make success, partial
  success, terminal failure, replay divergence, cancellation, and provider
  rejection unambiguous.
- Verdict: `fail`.
- Closure decision: `remains open`.
- Public evidence: representative terminal metadata and result helpers are
  present for repaired surfaces, but the current audit still marks the row
  `fail` because non-streaming `ExecuteResult.Text`, loop-synthesized stream
  fallback boundaries, provider-authored `MESSAGE.END`, PNIG
  `interaction.end`, CLI stop conditions, replay completion, and session
  `Done`/`Close` boundaries do not share one public terminal-authority rule.
- Affected declarations and outcomes: `agentloop.ExecuteResult`,
  `agentloop.Stream`, `agentloop.StreamingExecuteResult`,
  `messages.Message`, `messages.StreamMessage`, `messages.StreamMessageType`,
  `messages.Session`, `gateway.InferenceResponse`,
  `gateway.InteractionEvent`, `gateway.InteractionEventEnd`,
  `gateway.InteractionEventError`, and
  `gateway.InteractionEventCancellation`.
- Docs/tests/examples alignment: tests cover synthesized and in-band stream
  completion, selected session lifecycle paths, PNIG terminal events, CLI JSON
  and binary stream framing, replay divergence/incomplete/cancellation, and
  partial-output terminal metadata. They do not yet prove a single documented
  result/lifecycle contract for every public mode and helper.
- Reviewer commands: run `(cd go-agent-loop && go test ./pkg/engine ./test/functional)`
  to prove the execution and functional tests that distinguish synthesized
  completion from terminal failures; run `(cd go-llm-gateway && go test ./pkg/gateway)`
  to prove the gateway response and session lifecycle tests that expose the
  terminal-authority boundary.
- Exact next work: define caller-visible terminal states for `Execute`,
  `ExecuteStreaming`, provider-authored streams, synthesized fallback streams,
  `messages.Session.Done`, `messages.Session.Close`, replay completion,
  capture flush, and PNIG cancellation/error/end sequences; stage fixture and
  CLI replay/record compatibility updates with those terminal-authority rules.

### P4-API-04 - Provider Capability Discovery

- Checklist row: consumers can discover provider capabilities through public
  `go-llm-gateway` APIs without importing `go-agent-loop` runtime internals or
  concrete provider internals.
- Verdict: `pass`.
- Closure decision: `may close`.
- Public evidence: `go-llm-gateway/pkg/capabilities` defines the
  provider-neutral supported, unsupported, and unknown vocabulary;
  `providers.CapabilityReporter` and `gateway.CapabilityReporter` expose public
  discovery; `DefaultGateway.Capabilities()` and
  `DefaultSessionGateway.Capabilities()` return provider-reported capabilities
  or the documented unknown fallback; concrete Anthropic, OpenAI-compatible,
  Gemini, Grok, and fal.ai provider families report every public stateless and
  session capability field without live credentials.
- Affected declarations and outcomes: `providers.Provider`,
  `providers.SessionProvider`, `providers.CapabilityReporter`,
  `providers.ProviderCapabilities`, `providers.UnknownProviderCapabilities`,
  `gateway.CapabilityReporter`, `gateway.DefaultGateway.Capabilities`,
  `gateway.DefaultSessionGateway.Capabilities`, `providers.InferenceRequest`,
  `providers.ThinkingConfig`, and `providers.CacheControlConfig`.
- Docs/tests/examples alignment: README and development guidance describe
  supported, unsupported, and unknown states; deterministic tests cover
  provider-neutral fallback, gateway/session discovery, concrete provider
  capability reporting, overclaimed support checks, and fal streaming
  alignment. No live provider credentials are required.
- Reviewer commands: run
  `(cd agent-cli && go test ./internal/config ./internal/input ./internal/agent)`
  to prove the CLI config, input, and wiring tests that exercise the public
  capability-discovery path; run
  `(cd go-llm-gateway && go test ./pkg/capabilities ./pkg/gateway ./pkg/providers/openai ./pkg/providers/anthropic ./pkg/providers/gemini ./pkg/providers/fal ./pkg/providers/grok)`
  to prove provider-neutral capability fallback and concrete provider
  capability reporting; run `go doc ./go-llm-gateway/pkg/providers` to prove
  the exported provider capability vocabulary; run
  `go doc ./go-llm-gateway/pkg/gateway` to prove the gateway/session discovery
  surface exposed to consumers.
- Exact next work: none for provider capability discovery. Broader stream,
  model ownership, dependency/result/context, and API hygiene concerns remain
  tracked by their own non-pass rows.

### P4-API-05 - Stream And Session Semantics Across Boundaries

- Checklist row: streaming and session APIs document and preserve completion,
  cancellation, replay mismatch, provider-close, and error-classification
  semantics across provider and gateway boundaries.
- Verdict: `uncertain`.
- Closure decision: `remains open`.
- Public evidence: representative terminal-contract repair evidence has
  landed for direct gateway/provider streams, session close/error metadata,
  replay mismatch/incomplete handling, partial output, cancellation, and
  CLI-visible terminal fields. The current audit still identifies broader
  provider-wide stream/session parity, parser failure coverage, package
  ownership, and compatibility-staging items as open, so the row is not
  closable as a full public stream/session contract.
- Affected declarations and outcomes: `gateway.InferenceRequest`,
  `gateway.InferenceResponse`, `gateway.DefaultGateway`,
  `gateway.DefaultSessionGateway`, `gateway.InteractionRequest`,
  `gateway.InteractionEvent`, `inference.GatewayInferencer`,
  `inference.SessionGatewayInferencer`, `models.Message`,
  `models.SessionConfig`, `providers.InferenceRequest`,
  `providers.InferenceResponse`, and `messages.ErrorValue`.
- Docs/tests/examples alignment: `go-llm-gateway/README.md` and
  `docs/architecture/stream-terminal-contract.md` document terminal fields and
  caller guidance; gateway, inference, provider, replay, session, and CLI tests
  prove representative behavior. Evidence is still not exhaustive for every
  provider adapter, parser failure, fixture entrypoint, session helper, result
  helper, or package ownership boundary.
- Reviewer commands: run `(cd go-llm-gateway && go test ./pkg/inference ./pkg/gateway)`
  to prove the inference and gateway tests that preserve representative
  stream terminal ordering and session behavior; run
  `go doc ./go-llm-gateway/pkg/models` to prove the current compatibility-alias
  surface and its package-level documentation.
- Exact next work: declare whether `pkg/models` remains a compatibility alias
  layer or becomes gateway-owned vocabulary; standardize provider stream
  terminal ordering and session terminal status across every supported
  provider/session/replay surface; align docs, package comments, fixture
  validators, and CLI replay/record tests with that ownership and terminal
  contract.

### P4-API-06 - Local Unsupported-Feature Validation

- Checklist row: unsupported provider/request features fail locally before
  provider execution, with inspectable errors that identify the provider,
  requested feature or mode, and capability state.
- Verdict: `pass`.
- Closure decision: `may close`.
- Public evidence: the current head exposes `UnsupportedFeatureError` through
  provider and gateway packages; `DefaultGateway.Infer`,
  `DefaultGateway.InferStream`, `DefaultSessionGateway.ConnectSession`,
  `gateway.Interact`, and `inference.GatewayInferencer` have representative
  tests showing explicitly unsupported capabilities fail locally before
  provider side effects; unknown capabilities fall through without claiming
  support; fal streaming is a typed unsupported setup failure.
- Affected declarations and outcomes: `providers.UnsupportedFeatureError`,
  `gateway.UnsupportedFeatureError`, `gateway.DefaultGateway.Infer`,
  `gateway.DefaultGateway.InferStream`,
  `gateway.DefaultSessionGateway.ConnectSession`, `gateway.Interact`,
  `inference.GatewayInferencer`, `testing.WithReplayContext`, and
  CLI-local `config.ModelInfo` and MIME validation helpers.
- Docs/tests/examples alignment: gateway README/development guidance and the
  audit describe supported, unsupported, and unknown semantics; deterministic
  tests cover gateway/session validation, interaction/inferencer preservation,
  provider-local capability reporting, CLI-local model metadata and MIME
  validation, replay lifecycle cancellation, PNIG cancellation/timeout events,
  and fal unsupported streaming.
- Reviewer commands: run `(cd go-llm-gateway && go test ./pkg/testing ./pkg/gateway ./pkg/inference)`
  to prove the gateway, inference, and testing helpers that reject unsupported
  features before provider execution; run
  `(cd go-agent-loop && go test ./test/functional -run 'TestRun_ExitsOnContextCancellation|TestSession')`
  to prove the functional cases that demonstrate local cancellation and
  session validation behavior; run the `P4-API-04` provider capability
  commands when validating the capability state that drives local rejection.
- Exact next work: none for local unsupported-feature validation. Retry
  ownership, timeout ownership, and broader cross-surface cancellation
  documentation remain tracked by `P4-API-01`, `P4-API-03`, `P4-API-05`, and
  `P4-API-07`.

### P4-API-07 - API Hygiene, Dependency Ownership, And Compatibility Staging

- Checklist row: public constructors and composition seams keep filesystem,
  environment, process, network, transport, time, and provider runtime
  dependencies caller-owned or explicitly injected instead of hidden behind
  defaults.
- Verdict: `fail`.
- Closure decision: `remains open`.
- Public evidence: exported message, gateway, provider, interaction, fixture,
  and adapter declarations are inspectable and selected dependency seams are
  narrowed. The current audit still marks the row `fail` because compatibility
  ownership for `pkg/models`, overlapping gateway/provider request types, CLI
  internal composition exports, hidden side effects, fixture staging, and
  text-matching caller compatibility remain open.
- Affected declarations and outcomes: `messages.Message`,
  `messages.ContentPart`, `messages.StreamMessage`, `messages.ErrorValue`,
  `models.Message`, `models.SessionConfig`, `gateway.InferenceRequest`,
  `gateway.InteractionRequest`, `gateway.InteractionEvent`,
  `providers.Provider`, `providers.SessionProvider`,
  `providers.InferenceRequest`, `agent.Executor`, `agent.ProviderFactory`, and
  `services.SessionRunOptions`.
- Docs/tests/examples alignment: `go doc` exposes current public comments;
  interaction shape tests, gateway-to-loop adapter tests, fixture validators,
  provider runtime tests, and public docs narrow several risks. They do not yet
  establish complete package ownership, compatibility policy, hidden
  side-effect boundaries, or additive fixture/CLI migration rules.
- Reviewer commands: run `go doc ./go-agent-loop/pkg/messages` to prove the
  exported message and error vocabulary; run
  `go doc ./go-llm-gateway/pkg/gateway` to prove the gateway constructor and
  request ownership comments; run `go doc ./go-llm-gateway/pkg/models` to
  prove the compatibility alias surface; run
  `go doc ./go-llm-gateway/pkg/providers` to prove the provider ownership and
  capability comments; run `make typecheck` to prove the workspace still
  compiles after any compatibility-staging updates.
- Exact next work: add package-level compatibility notes that declare
  `pkg/models` as an alias layer or migrate it to gateway-owned vocabulary with
  adapters; document ownership between `gateway.InferenceRequest`,
  `providers.InferenceRequest`, and `models.*`; clarify that
  `agent-cli/internal/agent` and `agent-cli/internal/services` exports are
  application wiring rather than downstream APIs; stage future
  message/session/error changes additively with fixture validators, CLI
  replay/record tests, and legacy text compatibility.

## Gate Readiness And Closure Decisions

This section completes story
`phase-4-authoritative-convergence-validator-rerun-004` by evaluating
`P4-GATE-01` from the aggregate state of `P4-API-01` through `P4-API-07` and
by summarizing every requested row closure decision. The gate verdict is based
on the row evidence above, not on CI status alone.

### P4-GATE-01 - Phase 4 Public API Contract Readiness

- Checklist row: the Phase 4 public API contract rows are ready to close only
  when each requested API row has public implementation evidence, public
  docs/examples where required, deterministic credential-free validation, and
  a reviewer-runnable closure rationale.
- Verdict: `fail`.
- Closure decision: `remains open`.
- Public evidence: the aggregate row set has two closable pass rows and five
  rows that remain open. `P4-API-04` may close for provider capability
  discovery, and `P4-API-06` may close for local unsupported-feature
  validation. `P4-API-01`, `P4-API-02`, `P4-API-03`, `P4-API-05`, and
  `P4-API-07` remain open because the current authoritative evidence still
  names public contract gaps or incomplete reviewer-verifiable coverage. The
  current audit also states that whole-Phase-4 `P4-GATE-01` remains open until
  the remaining typed error/stream, dependency/result/context/lifecycle, API
  hygiene, and final validation repair lanes provide implementation evidence,
  docs/examples alignment where needed, deterministic validation, and a final
  validator decision that explicitly permits closure.
- Affected declarations and outcomes: all public surfaces named by the API row
  findings, including `agentloop.ExecuteResult`, `agentloop.Stream`,
  `messages.ErrorValue`, `messages.Session`, `gateway.DefaultGateway`,
  `gateway.DefaultSessionGateway`, `gateway.InferenceRequest`,
  `gateway.InteractionEvent`, `gateway.GatewayError`,
  `providers.UnsupportedFeatureError`, `providers.ProviderCapabilities`,
  `providers.InferenceRequest`, `models.Message`, `models.SessionConfig`,
  `agent.Executor`, and `services.SessionRunOptions`.
- Docs/tests/examples alignment: the report, audit, README guidance, stream
  terminal contract docs, exported declarations, and deterministic tests align
  for the two pass rows. The same evidence does not yet align enough to close
  the five non-pass rows, because those rows still name missing, partial, or
  representative-only public coverage rather than complete row closure.
- Reviewer commands: run `make typecheck` to prove the full workspace remains
  type-correct; run `make test` to prove the deterministic row-level tests
  still pass; run `make lint` to prove the codebase still satisfies the lint
  gates; run `make staticcheck` to prove static analysis still accepts the
  current public contract; run the row-specific commands listed under
  `P4-API-01` through `P4-API-07` to prove the individual public evidence.
  These commands prove reviewer-runnable quality and row evidence, but they do
  not convert non-pass rows into pass rows by themselves.
- Exact next work: keep whole-Phase-4 `P4-GATE-01` open and plan future repair
  lanes for the remaining open row work: context/timeout/dependency ownership
  for `P4-API-01`; provider-wide typed error and stream failure parity for
  `P4-API-02`; terminal result/lifecycle authority for `P4-API-03`;
  provider-wide stream/session parity plus package ownership reconciliation for
  `P4-API-05`; and API hygiene, dependency ownership, compatibility staging,
  and hidden-side-effect cleanup for `P4-API-07`.

### Requested Row Closure Summary

| Row | Verdict | Closure decision | Closure rationale |
| --- | --- | --- | --- |
| `P4-API-01` | `fail` | `remains open` | Public evidence still identifies prompt/config/filesystem loading, session/config/dialer side effects, full timeout/cancellation ownership, and stable dependency/result errors as open. |
| `P4-API-02` | `fail` | `remains open` | Representative typed-terminal evidence exists, but provider-wide and parser-wide parity for every adapter, helper entrypoint, and stream failure path is not publicly proven. |
| `P4-API-03` | `fail` | `remains open` | Representative terminal metadata exists, but public result and lifecycle authority is not yet documented and proven across every public mode and helper. |
| `P4-API-04` | `pass` | `may close` | Provider capability discovery has public capability vocabulary, reporter interfaces, gateway/session accessors, docs, and deterministic credential-free tests. |
| `P4-API-05` | `uncertain` | `remains open` | Stream/session terminal behavior is proven only for representative repaired surfaces, with provider-wide parity, parser failure coverage, package ownership, and compatibility staging still incomplete. |
| `P4-API-06` | `pass` | `may close` | Local unsupported-feature validation has public typed errors, gateway/session/interaction/inferencer coverage, capability-state evidence, docs, and deterministic credential-free tests. |
| `P4-API-07` | `fail` | `remains open` | Public API hygiene and dependency ownership still have unresolved package ownership, request overlap, CLI composition, fixture staging, hidden side effects, and text-compatibility work. |
| `P4-GATE-01` | `fail` | `remains open` | The gate cannot close while five API rows remain open; pass rows are not inferred from CI success and stale branch evidence is only advisory unless reproduced on this authoritative head. |

Rows marked `may close` cite public evidence and credential-free reviewer
commands in their row findings. Rows marked `remains open` are scoped to exact
future repair or cleanup work in the row findings and in the `P4-GATE-01`
aggregate next work above.

## Story 001 Closure

Story `phase-4-authoritative-convergence-validator-rerun-001` may close for
this PRD iteration because the report names the branch and commit under review,
records the authoritative baseline and typed-terminal reconciliation decisions,
and explains how the prior validator, batch 017, typed-terminal, and
`origin/main` evidence are reconciled against the current baseline.

This story does not provide row verdicts for `P4-API-01` through `P4-API-07` or
`P4-GATE-01`. Those remain assigned to later PRD stories.

## Story 002 Closure

Story `phase-4-authoritative-convergence-validator-rerun-002` may close for
this PRD iteration because the report now defines the required row finding
shape, verdict meanings, pass evidence threshold, fail and uncertain evidence
requirements, closure decision rules, and the no-repair boundary for this
validator.

This story does not evaluate `P4-API-01` through `P4-API-07` or `P4-GATE-01`.
Those row verdicts remain assigned to later PRD stories.

## Story 003 Closure

Story `phase-4-authoritative-convergence-validator-rerun-003` may close for
this PRD iteration because `P4-API-01` through `P4-API-07` now have
reviewer-grade verdicts based on current public API behavior, documented
contracts, deterministic credential-free tests, and reconciled prior evidence.
Pass rows cite public declarations and reviewer commands; non-pass rows name
affected public declarations or outcomes plus exact future repair or cleanup
work. `P4-GATE-01` remains assigned to story 004.

## Story 004 Closure

Story `phase-4-authoritative-convergence-validator-rerun-004` may close for
this PRD iteration because `P4-GATE-01` now has a reviewer-grade aggregate
verdict, every requested row has an explicit closure decision, pass rows cite
public evidence and credential-free commands, and open rows are scoped to exact
future repair or cleanup work. No row is marked closable from CI status, private
helper behavior, undocumented internals, or unreconciled stale branch evidence.

## Story 005 Closure

Story `phase-4-authoritative-convergence-validator-rerun-005` may close for
this PRD iteration because every row finding now includes reviewer commands
that explicitly state what behavior or evidence each command proves, and the
commands remain credential-free and reproducible from the authoritative head.
Pass rows still cite public evidence, and non-pass rows still keep exact next
work scoped to future repair or cleanup.

## Story 006 Closure

Story `phase-4-authoritative-convergence-validator-rerun-006` may close for
this PRD iteration because the report now recommends exactly one next planner
action: `repair`.

That recommendation is consistent with the row verdicts and closure decisions
above. `P4-API-04` and `P4-API-06` may close on the current authoritative
head, while `P4-API-01`, `P4-API-02`, `P4-API-03`, `P4-API-05`, and
`P4-API-07` remain open and already name exact future repair or cleanup work.
The gate verdict remains `fail`, so the correct single next planner action is
to repair the remaining open row work rather than start a new feature batch or
reframe the result as reconciliation-only cleanup.
