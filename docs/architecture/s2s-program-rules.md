# Speech-to-Speech Program — Standing Rules

Governing document for every lane in the speech-to-speech program, submitted as
**one batch containing the whole cross-phase DAG**: `batches/s2s-program.json`,
`requestId: operator-s2s-program-20260816` — 144 idea lanes plus one operator
loopback, 183 `DEPENDS_ON` edges, 35 zero-dependency roots, max depth 10.

There are no sequential batches. A `DEPENDS_ON` target must name a work in the
**same** batch, so one batch is what makes edges between the stages below legal
at all.

| Stage prefix | Lanes | Theme |
|---|---|---|
| `s2s-b0-*` | 1 | the realtime model allowlist — **blocks every live run in the program** |
| `s2s-b1-*` | 29 | gates, wire composition boundary, file decomposition, test-shape coverage |
| `s2s-b2-*` | 31 | deterministic clock, wire ports, transport seam, per-OS audio devices, audio primitives, both-side recording, observability, fixtures |
| `s2s-b3-*` | 32 | session capabilities (instructions, tools, images, turns), probe harness, parity contract, functional-test shaping |
| `s2s-b4-*` | 41 | WebRTC (10), verticals (28), fleet (3) |
| `s2s-e2e-*` | 6 | the acceptance chain: round trip → multi-turn → tool call → vision → observability → seed probe |
| `s2s-acc-*` | 4 | the blind probe fleet and its gate |

The `b1`/`b2`/`b3`/`b4` infixes are **topic prefixes, not phases**. Dependencies
cross them freely. Do not infer from your prefix that anything with a lower
number has merged — read your payload's `contention` field, which names your
actual upstreams.

## 0. The acceptance criterion this program exists to meet

> **Blind agents, deployed with the CLI in a random folder, can get it to do what
> they want.** We send a *fleet* of probes covering every milestone. We succeed
> when **all of them say it was easy, they understood it, and it worked.** We fail
> if any is **getting stuck**, or if something **broke**.

Every lane serves that. If your work does not, it is a leaf and must not gate
anything on the path to it.

**Blindness.** A probe gets three things: the binary name, a goal in plain
English, and an empty directory. Not the flag list, not the README, not this
document, not the source. It discovers the tool through `--help`, the files the
CLI creates for itself, and the errors it hits. **If a probe cannot discover a
capability, that is a product finding — a help or error-message gap — not a probe
bug.** Never hand a probe a hint; that converts discoverability into scripted
replay and invalidates the run.

**What this means for your lane, whatever your lane is:** the `--help` text and
the error messages you write are not documentation, they are the *product
surface the gate measures*. An error that says what went wrong, and what to do
instead, is the difference between a probe that recovers and a probe that gets
stuck. Write them for someone who has never seen this tool.

**Stuck is worse than failed.** A probe that hits a clear error, understands it,
and stops has found a *missing feature*. A probe that repeats the same broken
invocation, or re-reads `--help` without acting, has found a *confusing product*.
`s2s-acc-probe-stuck-detection` keeps those apart; do not conflate them.

**Objective and subjective are both required.** Reaching a goal is necessary but
not sufficient — a probe that succeeded while rating the experience `confusing` is
a **failing** result for the gate. Succeeding despite the product is not success.

**The gate is not tunable.** Lowering a turn budget, dropping a goal, or accepting
`workable` as a pass makes the criterion decoration. Unreasonable goals go in a PR
comment and stay failing.

The first live fleet run is **expected to fail**. Its ranked friction list —
verbatim quotes of what confused each probe — is the work queue. Those findings
become new lanes through the `s2s-program-loopback` token; they are never fixed
inside the gate lane.

### The model

**`gpt-realtime-2.1-mini`.** Facts, recorded here so no lane re-derives them
(source: `https://developers.openai.com/api/docs/models/gpt-realtime-2.1-mini`):

| Property | Value |
|---|---|
| Model ID | `gpt-realtime-2.1-mini` |
| Modalities | audio in/out, text in/out, **image input** |
| Context / max output | 128,000 / 32,000 tokens |
| Knowledge cutoff | 2024-09-30 |
| Endpoints | **Realtime API (`v1/realtime`) only** — WebSocket, WebRTC, SIP. Chat Completions, Responses, Assistants and Batch are **not** supported |
| Tools | function calling enabled; prompt caching supported |

Image input is a **first-class realtime capability** — do not route vision
through a second model or a non-realtime endpoint.

Gate on **capability metadata**, never on a model-name string comparison. The
metadata lands in `s2s-b0-realtime-model-allowlist`.

## The spine — 18 lanes to the acceptance gate

Each carries a `SPINE LANE` instruction telling it to deliver exactly its ask,
defer every adjacent improvement to a PR comment, and land fast.

```
realtime-model-allowlist (d0) ──────────────┬──────────────────────────┐
wavio-package (d0) ─┐                       │                          │
                    ├─> audio-source-and-sink (d1) ─┬─> --audio-in  (d2) ─┐
split-services-session (d0) ─┬─────────────────────>┴─> --audio-out (d2) ─┴─> e2e-audio-roundtrip-proof (d3)
                             ├─> session-agents-md (d1) ─────────┐        │
                             ├─> session-image-input (d1) ───────┼────────┘
                             └─> session-turn-management (d1) ───┼──> e2e-multiturn-conversation (d4)
                                                                 │            │
                              e2e-tool-call-conversation (d5) <───┴────────────┤
                              e2e-vision-describe        (d5) <────────────────┘
                                          └──> e2e-conversation-observability (d6)
                                                        └──> e2e-customer-acceptance-probe (d7)   [probe mechanism, blind]
                                                                    ├──> acc-probe-goal-catalog    (d8)
                                                                    ├──> acc-probe-stuck-detection (d8)
                                                                    └──────┴──> acc-probe-friction-report (d9)
                                                                                    └──> acc-fleet-gate (d10)  <-- ACCEPTANCE
```

**`s2s-b0-realtime-model-allowlist` blocks the entire program.**
`agent-cli/internal/services/session.go:28` declares
`openAIRealtimeModel = "gpt-realtime"` and line 611 tests membership with a single
`strings.EqualFold`, so `gpt-realtime-2.1-mini` is rejected at `session.go:605`
**before any network dial**. Until that lane merges, every live run fails before
it opens a socket. It is roughly ten production lines.

### Carry-over, not greenfield

Three capabilities the acceptance criterion needs already exist and are tested —
on the `ask` path, not the realtime path. **Reuse them; do not write second
implementations.**

| Capability | Already exists at | Carried over by |
|---|---|---|
| `AGENTS.md` → system prompt | `agent-cli/internal/workspace/agents_md.go`; proven reaching the provider at `test/integration/ask_test.go:208-248` | `s2s-b3-session-agents-md` |
| Image MIME detection | `agent-cli/internal/input/files.go:13-14`, `internal/config/models.go:132-133` | `s2s-b3-session-image-input` |
| Tool executor | the non-realtime path | `s2s-b3-session-tool-executor-wiring` |

The realtime loop is built at `services/session.go:173-177` with **only**
`WithMode(engine.DuplexSession)` and `WithSessionInferencer` — no instructions,
no tools, no files. That single construction site is why a voice session today
cannot be steered, cannot call a tool, and cannot see an image.

### Every acceptance claim needs a negative control

These are mandatory, not hardening. Without them the chain passes against a
system doing nothing useful:

| Milestone | Control that must fail |
|---|---|
| `e2e-audio-roundtrip-proof` | RMS above the VAD threshold — a well-formed empty WAV must fail |
| `e2e-multiturn-conversation` | an early turn's fact must influence a later turn, else it is N unrelated sessions |
| `e2e-tool-call-conversation` | the **no-tools AGENTS.md profile** yields zero invocations for the identical request |
| `e2e-vision-describe` | the identical question with **no image** must not produce the image-only token |
| `e2e-conversation-observability` | reconstruction from **null-subject artifacts** must fail |
| `e2e-customer-acceptance-probe` | the probe reports **failure** against a null agent |

Goals are verified against **recorded artifacts** — the tool record's exact
arguments, the image record's byte length — never against a probe's own opinion
of how the conversation went.

If you are **not** on the spine, do not add work to it. In particular the
18 `cov-*` lanes are DAG leaves with zero dependents by design — they fill idle
executor slots and must never become a gate for anything.

Every lane payload's `rules` field points here. This path is **tracked**, so it
is present in every worktree branched from `main` — verify with
`ls docs/architecture/s2s-program-rules.md` from your worktree root before
trusting any pointer in your payload.

Program design: `../../../designs-finalization.md` (harness root, outside this repo).

**Customer surface.** The `## Customer experience` section at the top of
`designs-finalization.md` is the contract every lane builds toward: the exact
`agent session` / `agent devices` / `agent media` invocations, the device
resolution table, the recording directory layout, and the external-media-source
(go2rtc / RTSP / Tuya) path. If your lane's behavior would contradict anything
written there, that is a design question — raise it in a PR comment, do not
resolve it in code.

**Patterns lifted from `infinite-you`.** Four lanes replicate an existing,
readable pattern rather than inventing one. Read the model before writing code;
your payload names it. Path prefix: `C:/Users/andre/work/portos/infinite-you/`.

| Pattern | Model | Replicated by |
|---|---|---|
| Wire composition boundary — explicit named ports, `validateDependencies` naming the nil port, `CompositionOption`, construction is inert | `pkg/services/factory_definitions/wire/` | `s2s-b1-wire-composition-boundary`, then `s2s-b2-wire-ports-*` |
| Deterministic clock — `Source`, `Real`, `Deterministic` mapping logical ticks to stable timestamps, `Ensure` | `pkg/platform/clock/clock.go` | `s2s-b2-platform-clock` |
| Both-side wire transcript — `Peer{client,agent}` × `Direction{in,out}`, **verbatim bytes never re-encoded**, `tee` | `pkg/platform/wiretranscript/` | `s2s-b2-transcript-*` (5 lanes) |
| Parity contract — `Projection`, per-interface `Normalize*`, `Compare → []Difference` with stable paths, `NormalizationError` instead of defaulting, exclusion list proven by **paired mutations** | `tests/functional/sessionparity/` | `s2s-b3-parity-*` (4 lanes) |
| Functional layout by subject + `functional-quarantine.json` + `_long_` naming | `tests/functional/` | `s2s-b3-functional-layout-and-quarantine` |

**Lane sizing contract.** Every lane in this program is deliberately small: one
story, one surface, ≤~400 changed lines, finishable in one or two process
sessions. If your lane looks larger than that when you plan it, say so in a PR
comment — do not silently widen it, and do not pull in an adjacent lane's files
to "finish the thought".

---

## 1. Delivery contract

A lane is done when: required CI is **terminal green**, blocking PR
conversation comments are addressed, merge conflicts are reconciled, and the
**PR is merged**.

Stage ownership — do not blur these:

| Stage | Finish line |
|---|---|
| **process** (implementer) | final head pushed + PR open + required CI **started** + feedback addressed |
| **review** | terminal CI + **merge** |

If your PRD's acceptance criteria mention "merged", that is the lane's overall
finish line owned by review — it is never a reason for the implementer to keep
looping on CI.

**Evidence about a CI run goes in a PR comment, never in a commit.** A commit
describing a run creates a new head, invalidates the run it describes, and
restarts CI. Post comments from a file (`gh api -X POST repos/<o>/<r>/issues/<n>/comments -F body=@file.md`);
never pass markdown inline in a double-quoted string — backticks get
command-substituted and silently vanish from the published comment.

---

## 2. Surface ownership

One lane owns each surface. If you need to change a file you do not own, say so
in your PR and stop — do not edit it.

### Stage b1 — gates and build

| Surface | Owner lane |
|---|---|
| `Makefile` (`test-hermetic` target only), `.github/workflows/ci.yml`, `docs/architecture/testing-tiers.md` | `s2s-b1-hermetic-test-target` |
| `tools/coveragegate/**`, `coverage-manifest.json` | `s2s-b1-coverage-manifest-gate` |
| `tools/timingate/**`, `Makefile` (`test-budget` target only) | `s2s-b1-wallclock-budget-gate` |
| `.golangci.yml`, `docs/architecture/size-baselines.md` | `s2s-b1-file-and-func-size-lint` |
| `agent-cli/internal/wire/**` | `s2s-b1-wire-composition-boundary` (`s2s-b1-*`), then extended — never redesigned — by `s2s-b2-wire-ports-transport`, `s2s-b2-wire-ports-audio`, `s2s-b2-wire-mock-injection` |

Three lanes touch `Makefile`. Each owns **exactly one new target** and appends it
at the end of the file; none may reformat, reorder, or edit an existing target.
That keeps the rebase conflicts to a trailing-hunk resolution.

### Stage b1 — decomposition (pure mechanical, no behavior change)

| File being split | Owner lane |
|---|---|
| `go-agent-loop/pkg/messages/agent_messages.go` | `s2s-b1-split-messages-agent-messages` |
| `agent-cli/internal/agent/executor.go` | `s2s-b1-split-agent-executor` |
| `agent-cli/internal/services/chat.go` | `s2s-b1-split-services-chat` |
| `agent-cli/internal/services/session.go` | `s2s-b1-split-services-session` |
| `go-llm-gateway/pkg/providers/openai/session.go` | `s2s-b1-split-openai-session` |
| `agent-cli/internal/services/session_runtime.go` | `s2s-b1-split-services-session-runtime` |

Two split lanes are in `agent-cli/internal/services`. They are file-disjoint
(`chat.go` vs `session.go` vs `session_runtime.go`) and must stay that way: a
split lane may **not** touch a file it is not named for, even to fix an import.

### Stage b1 — test-shape coverage

| Surface | Owner lane |
|---|---|
| `cli/{root,routes,command_interface}.go` + their tests | `s2s-b1-cov-cli-root-and-routes` |
| `cli/chat.go` + its test | `s2s-b1-cov-cli-chat-command` |
| `cli/ask.go`, `cli/tool.go` + their tests | `s2s-b1-cov-cli-ask-and-tool-commands` |
| `cli/config.go`, `cli/interaction.go` + their tests | `s2s-b1-cov-cli-config-and-interaction` |
| `agent-cli/internal/logger/**` | `s2s-b1-cov-logger-routing` |
| `services/ask.go`, `services/autocomplete.go` + their tests | `s2s-b1-cov-services-ask-and-autocomplete` |
| `tools/tool_shell.go` + its test | `s2s-b1-cov-tools-shell-injected-process` |
| `tools/edit.go`, `tools/tool_filesystem.go` + their tests | `s2s-b1-cov-tools-filesystem-and-edit` |
| `tools/{registry,adapter,base,result}.go` + a shared conformance suite | `s2s-b1-cov-tools-registry-contract` |
| `tools/tool_screen*.go`, `tools/tool_mouse*.go` + their tests | `s2s-b1-cov-tools-screen-and-mouse-platform` |
| `messages/buffers.go` + its test/fuzz corpus | `s2s-b1-cov-messages-buffers-property` |
| `messages/reconstruction.go` + its test and goldens | `s2s-b1-cov-messages-reconstruction-golden` |
| `agent-cli/internal/session/**` | `s2s-b1-cov-session-storage` |
| `agent-cli/internal/workspace/**` | `s2s-b1-cov-workspace-agents-md` |
| `gw/pkg/providers/{errors,provider,session_provider}.go` + their tests | `s2s-b1-cov-providers-error-taxonomy` |
| `gw/pkg/models/**` | `s2s-b1-cov-models-session-contract` |
| the files produced by the executor split + their tests | `s2s-b1-cov-agent-executor-core` |
| the files produced by the chat split + their tests | `s2s-b1-cov-services-chat-core` |

**`cli/session.go` is owned by no `s2s-b1-*` lane.** The `s2s-b3-*`
session-capability lanes own it — ten of them, each adding exactly one flag in
alphabetical position. No coverage lane may add a test that pins its current flag
set — those tests would be rewritten within days.

### Stage b2 — platform, time, transport, recording

| Surface | Owner lane |
|---|---|
| `go-agent-loop/pkg/platform/clock/**` | `s2s-b2-platform-clock` |
| `go-agent-loop/test/functional/timeharness/**` | `s2s-b2-functional-time-harness` |
| `agent-cli/internal/wire/**` (transport port) | `s2s-b2-wire-ports-transport` |
| `agent-cli/internal/wire/**` (audio + clock ports) | `s2s-b2-wire-ports-audio` |
| `agent-cli/internal/wire/**` (mock-injection initializer) | `s2s-b2-wire-mock-injection` |
| `gw/pkg/transport/**` | `s2s-b2-transport-interface` |
| `gw/providers/grok/dialer.go` | `s2s-b2-transport-grok-retype` |
| `gw/providers/openai/realtime_dialer.go`, `services/session_runtime_openai.go` (adapter deletion) | `s2s-b2-transport-openai-retype` |
| `gw/pkg/testing/session_websocket_dialer.go` | `s2s-b2-transport-recordreplay-retype` |
| `agent-cli/internal/audio/device*.go` (registry contract) | `s2s-b2-device-registry-contract` |
| `agent-cli/internal/audio/device_virtual*.go` | `s2s-b2-device-virtual-loopback` |
| `agent-cli/internal/audio/device_windows*.go` | `s2s-b2-device-windows-wasapi` |
| `agent-cli/internal/audio/device_darwin*.go` | `s2s-b2-device-macos-coreaudio` |
| `agent-cli/internal/audio/device_linux*.go` | `s2s-b2-device-linux-alsa-pulse` |
| `agent-cli/internal/audio/device_select*.go` | `s2s-b2-device-selection-and-fallback` |
| `gw/pkg/wavio/**` | `s2s-b2-wavio-package` |
| `gw/pkg/wavio/resample*.go` | `s2s-b2-audio-resampler` |
| `scripts/gen-audio-corpus.ps1`, `go-agent-loop/testdata/audio/**` | `s2s-b2-audio-corpus-generator` |
| `go-agent-loop/pkg/audiofixture/**` | `s2s-b2-audiofixture-loader` |
| `agent-cli/internal/audio/source_file.go`, `sink.go`, `sink_file.go` | `s2s-b2-audio-source-and-sink` — **SPINE**, file path only |
| `agent-cli/internal/audio/source_device.go`, `sink_device.go` | `s2s-b2-audio-device-source-and-sink` — split out so the file path never waits on devices |
| `go-agent-loop/pkg/transcript/frame*.go` | `s2s-b2-transcript-frame-format` |
| `go-agent-loop/pkg/transcript/writer*.go`, `tee*.go` | `s2s-b2-transcript-writer-and-tee` |
| `go-agent-loop/pkg/transcript/client*.go` | `s2s-b2-transcript-client-side` |
| `go-agent-loop/pkg/transcript/agent*.go` | `s2s-b2-transcript-agent-side` |
| `go-agent-loop/pkg/transcript/manifest*.go` | `s2s-b2-transcript-manifest-and-artifacts` |
| `go-agent-loop/pkg/metrics/**` | `s2s-b2-metrics-package` |
| `go-agent-loop/pkg/logging/**` | `s2s-b2-buffer-crossing-logs`, then `s2s-b2-logging-alloc-gate` (sequential, gated by DEPENDS_ON) |
| `gw/internal/sessionfixturevalidator/**` | `s2s-b2-fixture-validator-shapes` |
| `Makefile` (`record-fixtures` target only), session fixture `testdata/**` | `s2s-b2-record-fixtures` |

Seven lanes share `agent-cli/internal/audio`. They are file-disjoint by the
`device_<backend>*.go` naming above, and the three per-OS backends carry mutually
exclusive build tags, so they cannot collide even when they merge on the same day.
Every backend runs the **same conformance suite** owned by
`s2s-b2-device-registry-contract` — do not write a bespoke test file per OS.

Three lanes extend `agent-cli/internal/wire`, which `s2s-b1-wire-composition-boundary`
established. They **register ports within that structure**; none of them may
redesign the boundary. If you believe the shape is wrong, say so in a PR comment
and stop on that item.

### Stage b3 — session capabilities, probes, parity

| Surface | Owner lane |
|---|---|
| `services/session_live.go` (prompt block) | `s2s-b3-session-text-prompt` |
| `services/session_live.go` (loop construction) | `s2s-b3-session-tool-executor-wiring` |
| `services/session_options.go` (tool defaults) | `s2s-b3-session-default-toolset` |
| `services/session_options.go` (tool fields) | `s2s-b3-session-tool-definitions` |
| `services/session_audio_in.go` | `s2s-b3-session-audio-in-file` |
| `services/session_audio_in_device.go` | `s2s-b3-session-audio-in-device` |
| `services/session_audio_out.go` | `s2s-b3-session-audio-out-file` |
| `services/session_audio_out_device.go` | `s2s-b3-session-audio-out-device` |
| `services/session_vad.go` | `s2s-b3-session-vad-gating` |
| `services/session_duration.go` | `s2s-b3-session-max-duration` |
| `agent-cli/internal/cli/devices.go` | `s2s-b3-devices-list-command` |
| `services/session_recording.go` | `s2s-b3-session-recording-flags` |
| `agent-cli/test/functional/smoke/audio_roundtrip_test.go` + its fixtures | `s2s-e2e-audio-roundtrip-proof` — **milestone**; may not modify `session_audio_*.go` |
| `agent-cli/internal/cli/session_replay.go`, `session_parity.go` | `s2s-b3-session-replay-and-parity-commands` |
| `agent-cli/test/functional/parity/projection*.go` | `s2s-b3-parity-projection` |
| `agent-cli/test/functional/parity/normalize_client*.go` | `s2s-b3-parity-normalize-client` |
| `agent-cli/test/functional/parity/normalize_agent*.go` | `s2s-b3-parity-normalize-agent` |
| `agent-cli/test/functional/parity/compare*.go` | `s2s-b3-parity-compare-and-report` |
| `agent-cli/internal/probe/scenario*.go` | `s2s-b3-probe-scenario-model` |
| `agent-cli/internal/probe/expect*.go` | `s2s-b3-probe-expectations`, `s2s-b3-probe-dead-session-guard` |
| `agent-cli/internal/probe/sequence*.go` | `s2s-b3-probe-logical-time-sequencing` |
| `agent-cli/internal/probe/transport_replay.go` | `s2s-b3-probe-replay-transport` |
| `agent-cli/internal/probe/transport_live.go` | `s2s-b3-probe-live-transport` |
| `agent-cli/internal/probe/runner*.go`, `result*.go` | `s2s-b3-probe-runner-jsonl` |
| `agent-cli/internal/cli/probe.go` | `s2s-b3-probe-cli-surface` |
| `agent-cli/test/functional/**` layout + `functional-quarantine.json` | `s2s-b3-functional-layout-and-quarantine` |
| `go-agent-loop/test/functional/session_harness.go` | `s2s-b3-session-harness-extension` |
| `go-agent-loop/test/functional/duplex_fixture*.go` | `s2s-b3-duplex-functional-time-fixture` |

`cli/session.go` is shared by all ten session-capability lanes — each adds **its
own flag and nothing else**, inserted in **alphabetical position** within the
existing flag block so concurrent lanes conflict on distinct hunks.

`s2s-b3-session-harness-extension` **extends** the existing harness. It is at
87.7% coverage and is the designated reuse target for the whole program — forking
it, or reimplementing `MockSessionInferencer` / `SessionScenario`, fails the lane.

### Stage b4 — WebRTC, verticals, fleet

| Surface | Owner lane |
|---|---|
| `gw/pkg/transport/rtc/contract*.go` | `s2s-b4-rtc-transport-contract` |
| `gw/pkg/transport/rtc/signaling*.go` | `s2s-b4-rtc-signaling` |
| `gw/pkg/transport/rtc/peerconn*.go` | `s2s-b4-rtc-peerconnection-lifecycle` |
| `gw/pkg/transport/rtc/track_egress*.go` | `s2s-b4-rtc-audio-track-egress` |
| `gw/pkg/transport/rtc/track_ingress*.go` | `s2s-b4-rtc-audio-track-ingress` |
| `gw/pkg/transport/rtc/device_bind*.go` | `s2s-b4-rtc-device-binding` |
| `gw/pkg/transport/rtc/mediasource*.go`, `agent-cli/internal/cli/media.go` | `s2s-b4-rtc-external-media-source` |
| `gw/pkg/transport/rtc/record*.go` | `s2s-b4-rtc-recording` |
| `agent-cli/internal/cli/session.go` (rtc flags only) | `s2s-b4-rtc-cli-surface` |
| `agent-cli/test/functional/parity/transport_parity*.go` | `s2s-b4-rtc-transport-parity` |
| `agent-cli/internal/probe/scenarios/<one file>` + one named test file | each `s2s-v*` lane, one file each |
| `agent-cli/internal/probe/fleet/**` | `s2s-b4-fleet-composer` |
| `agent-cli/internal/probe/fault/**` | `s2s-b4-fault-injection` |
| `agent-cli/internal/probe/report/**` | `s2s-b4-fleet-summary-artifact` |

Vertical lanes are file-disjoint by construction: **one scenario file, one test
file, nothing else.** A vertical that needs a production-code change has found a
gap in an upstream lane — report it in a PR comment and stop on that item rather than
editing the production file.

WebRTC is **implementation work, not a design spike**. `s2s-b4-rtc-transport-contract`
records the pion-versus-go2rtc decision with its reasons, and the nine lanes after
it build on that decision; none of them may relitigate it.

---

## 3. Contention protocol

- **Rebase onto `origin/main` immediately before your final push**, when GitHub
  reports a real conflict, or when the reviewer asks — not every time main moves.
- **Never hand-merge generated files.** Rerun the generator on the rebased tree
  and commit its output. This includes `wire_gen.go`.
- Merged `main` is authoritative. `MERGEABLE` means "no conflicts", **not**
  "up to date with main". Before claiming a merged upstream fix is in your tree,
  prove it: `git merge-base --is-ancestor <fix-sha> HEAD`, and decisively
  `git show HEAD:<path> | grep -c <identifier-the-fix-introduced>` compared
  against `git show origin/main:<path> | grep -c ...`.
- `prd.json` and `progress.txt` are untracked worktree scaffolding and must
  **never** appear in your PR diff. Never `git add -f` them.

---

## 4. Test discipline

Tiers and their commands:

| Tier | Command | Runs |
|---|---|---|
| T0 unit + functional | `make test`, `make test-integration` | every PR |
| T0 hermetic (no CGO, no mic) | `make test-hermetic` | every PR + local Windows |
| T1 probe-replay | `make test-probe` | every PR |
| T2 probe-live | `agent probe run <scenario> --transport live` | **your lane's acceptance evidence**, never PR CI |
| T3 fleet + fault | `agent probe fleet` | on demand |

### 4.1 Test-shape taxonomy

Your payload names the shapes your lane must apply. A lane that raises a
coverage number without producing its named shapes is rejected in review, even
if the number moves — accessor tests move numbers and prove nothing.

| # | Shape | What it means | Your lane fails review if |
|---|---|---|---|
| **S1** | Command-execution | Build the cobra root via the real `Router`, execute with actual argv, assert stdout, stderr and exit code | you call a handler function directly when a command path exists |
| **S2** | Flag-matrix table | Table-driven over flag combinations **including invalid and conflicting ones**; assert the typed error | only the happy combination is covered |
| **S3** | Golden-file | Output compared against a committed golden, with an `-update` regeneration flag | you regenerate the golden to make a change pass |
| **S4** | Error-path table | One row per error branch, asserting error **identity** (`errors.Is` / typed) and the message contract | rows assert only `err != nil` |
| **S5** | Injected-effect | Process, network or clock effects replaced at the seam that already exists in the code | the test spawns a real process or touches the network |
| **S6** | Filesystem sandbox | Real filesystem under `t.TempDir()`, covering permission and missing-path branches | you mock the filesystem instead of using a temp dir |
| **S7** | Property / fuzz | Go native fuzzing or randomized input asserting **invariants** | the "invariants" are restated constants |
| **S8** | Race / concurrency | `-race`, concurrent producers and consumers, invariants asserted under load | concurrency is simulated sequentially |
| **S9** | Leak-check | Goroutine count and open handles compared before and after, with a settle tolerance | teardown is asserted only by "returned no error" |
| **S10** | Allocation-benchmark gate | `testing.B` + `ReportAllocs`, failing above a committed budget | the budget is set above the measured value, so it can never fail |
| **S11** | Conformance | **One shared suite** run against every implementation of an interface | each implementation gets bespoke one-off assertions |
| **S12** | Platform-guarded | Build-tag/GOOS-guarded, with a **recorded** skip reason per platform | a platform is skipped silently and the package is claimed covered |
| **S13** | Replay-divergence | The assertion *is* the replay dialer rejecting a mismatched outbound payload | you edit a fixture to make the test pass |
| **S14** | Probe-scenario | One scenario file; the replay run gates CI, the live run is the acceptance evidence | you fork the scenario per transport |
| **S15** | Logical-time sequencing | Both sides share one `Deterministic` clock; assertions are on **logical ticks**, not wall duration | the test sleeps, polls, or asserts on elapsed wall time |
| **S16** | Both-side parity | Client and agent transcripts normalized to one `Projection` and `Compare`d; every difference is a finding | only one side is recorded, or the exclusion list has no paired mutation test |
| **S17** | Device round-trip | Real capture device → session → real playback device, asserted on emitted energy and recognized transcript | a virtual device is substituted while claiming real-device coverage |

On S15, S16 and S17 specifically:

- **S15**: `time.Sleep` in a sequenced test is a defect, not a workaround. Advance
  the shared clock instead. The functional-time harness fails a test that sleeps
  on its goroutine, so this is enforced, not merely requested.
- **S16**: the exclusion list is only trustworthy if it is proven in **both**
  directions. A test that mutates an excluded field and requires an unchanged
  projection is half the work; without the **paired** test that mutates a
  *retained* field inside the same real capture and requires a *detected*
  difference, an over-broad exclusion list silently passes everything. Both halves
  or the lane is not done.
- **S17**: a skipped device test must record **why** — which platform, which
  missing capability. Substituting the virtual backend and reporting device
  coverage is a false claim, and the virtual backend exists precisely so you never
  need to.

Hard rules:

1. **Prove through public surfaces** — `agent session …`, `agent probe run …`,
   emitted stream events. Never reach into an internal helper to fake a
   capability the CLI does not expose.
2. **Silence is not success.** Every assertion set must include a positive
   signal (audio energy present, transcript non-empty, event ordering, exact
   counts). An assertion set that would pass against a dead session is rejected
   in review.
3. **One scenario, two transports.** A scenario file must run unmodified under
   `--transport replay` and `--transport live`. Do not fork it.
4. **Reuse before invent.** These already exist and must not be reimplemented:
   `go-agent-loop/test/functional/session_harness.go` (MockSessionInferencer,
   SessionScenario), `go-llm-gateway/pkg/testing/session_*` (recording and
   replay dialers with outbound-divergence detection),
   `internal/sessionfixturevalidator`, `agent-cli/test/integration/setup.go`.
5. **Never invoke the TTS binary from a test.** Audio input loads from the
   committed corpus via `go-agent-loop/pkg/audiofixture`, hash-asserted.
6. **No meta tests.** Do not scan source files, validate doc link topology,
   inspect bundle internals, or enforce command/route inventories.
7. **PR tier budget is 60 s total** (T0 + T1). If your lane pushes it over, that
   is your lane's problem to solve before merge.

---

## 5. Coverage manifest discipline

The manifest is a **closed set**: every measured package needs an entry, or the
gate fails naming that package.

- Register the **measured** value, never an unmeasured placeholder.
- Minimums use **exactly two decimal places** (`80.0` is rejected; write `80.00`).
- The `packages` array stays **sorted** by import path.
- Each entry carries **exactly one** of `minimum` or `exception`.
- A refactor that moves code into a nested `internal/` subdirectory creates a
  **new measured package** — Go treats `.../foo/internal` as separate from
  `.../foo`, and the parent entry does not cover the child. Enumerate every new
  package in one pass before pushing; the gate returns on the first unregistered
  package, so fixing them one at a time costs one full CI cycle each.
- **Never** run manifest auto-regeneration. It rewrites entries other lanes own.
- Only edit entries for packages your own diff moved.

---

## 6. Measured baselines (2026-08-15)

Measured with `CGO_ENABLED=0 go test -tags=nomicrophone ./... -cover` per module.
All green. Total wall clock ≈ 25 s. **Use these as your before-numbers; do not
re-measure to discover them.**

| Package | Coverage |
|---|---|
| `go-agent-loop/pkg/state` | 100.0% |
| `go-agent-loop/test/functional` | 87.7% |
| `go-llm-gateway/pkg/providers/anthropic` | 89.9% |
| `go-llm-gateway/pkg/providers/fal` | 85.4% |
| `go-llm-gateway/internal/sessionfixturevalidator` | 85.1% |
| `go-llm-gateway/pkg/providers/openai` | 82.1% |
| `go-agent-loop/pkg/subsystems` | 80.7% |
| `go-llm-gateway/pkg/providers/gemini` | 78.0% |
| `go-llm-gateway/pkg/inference` | 77.0% |
| `go-llm-gateway/pkg/gateway` | 74.5% |
| `agent-cli/test/integration` | 71.4% |
| `go-agent-loop/pkg/agentloop` | 70.7% |
| `go-agent-loop/pkg/engine` | 70.4% |
| `agent-cli/internal/skills` | 69.6% |
| `agent-cli/internal/testtiming` | 68.5% |
| `go-agent-loop/pkg/participants` | 67.6% |
| `go-llm-gateway/pkg/capabilities` | 66.7% |
| `agent-cli/internal/config` | 65.0% |
| `agent-cli/internal/output` | 64.8% |
| `go-llm-gateway/pkg/providers/grok` | 63.6% |
| `go-llm-gateway/pkg/testing` | 59.0% |
| `go-agent-loop/pkg/messages` | 55.6% |
| `agent-cli/internal/input` | 52.5% |
| `go-llm-gateway/pkg/providers` | 41.5% |
| `agent-cli/internal/tools` | 40.8% |
| `agent-cli/internal/agent` | 40.2% |
| `agent-cli/internal/session` | 31.6% |
| `agent-cli/internal/services` | 27.6% |
| `agent-cli/internal/logger` | 7.7% |
| `agent-cli/internal/cli` | 7.1% |
| `go-agent-loop/pkg/logging`, `go-llm-gateway/pkg/models`, `agent-cli/internal/{wire,workspace,execctx,flags,sysinfo}` | 0.0% |

Known environment facts:

- With **CGO enabled on Windows**, `agent-cli/{cmd/agent, internal/audio,
  internal/cli, internal/wire, test/integration}` fail to build
  (`runtime/cgo: cgo.exe: exit status 2`). Use `make test-hermetic`.
- Linux CI has a C toolchain, so it does **not** reproduce that failure.
- The harness-root `credentials` file is **0 bytes**. Live credentials come from
  `AGENT_MODEL__*` env vars or `~/.agent-cli/config.yaml`. Never read that file
  and never commit a credential.

---

## 7. Known gaps (do not rediscover these)

- `runAgentLoopSession` (`agent-cli/internal/services/session.go:173`) passes no
  `ToolExecutor` — realtime tool calls cannot execute today.
- `SessionRunOptions` and `agent session` expose **no** audio input or output;
  `internal/audio`'s `MicrophoneSource`, `SliceSource` and VAD are unreachable
  from the CLI.
- `pkg/testing` record/replay dialers are typed to `grok.WebSocketDialer`;
  OpenAI is adapted through `openAIWebSocketDialerAdapter`
  (`session_runtime.go:686-712`).
- There is no metrics package.
- The committed realtime fixture corpus is 8 small, text-dominated files with no
  audio-in, no tool call, and no barge-in.
- Zero `webrtc` / `go2rtc` / `pion` references exist in any Go file. The `go2rtc/`
  checkout at the harness root is vendored upstream and is not on any code path.
- **Recording is one-sided.** `pkg/testing` records the dialer's view only.
  Nothing captures the client side independently, so "we never sent it" and "they
  never received it" are indistinguishable from a capture. The five `s2s-b2-transcript-*`
  `transcript-*` lanes fix this.
- **There is no time abstraction.** Nothing maps logical ticks to stable
  timestamps, so bidirectional tests cannot be sequenced deterministically and
  every duplex assertion today would be a wall-clock race.
- **`agent-cli/internal/wire` measures 0.0%** and has no composition-boundary
  discipline — no explicit port list, no dependency validation, no composition
  options — so each consumer constructs differently.
- **There is no device enumeration at all.** `agent devices list` does not exist,
  and the working `MicrophoneSource` / `SliceSource` / energy VAD in
  `internal/audio` (already **89.2%** covered) are unreachable from `agent session`.
  The primitives are sound; only the CLI reach is missing.

---

## 8. Escalation

If you believe a rule here is wrong, or your lane cannot proceed without
touching a surface another lane owns, say so plainly in a PR comment and stop
on that item — finish everything else in your lane first. Do not silently widen
your lease.

## 9. LocalAI, the local realtime tier, and the licence rule

Added 2026-08-16, alongside the `operator-s2s-localai-20260816` addendum batch
(six `s2s-lai-*` lanes). That batch runs concurrently with this one and carries
no `DEPENDS_ON` edges into it, because a dependency target must live in the same
batch. Cross-batch coupling is recorded in each payload's `contention` field
instead.

### 9.1 Why a third tier exists

Every realtime test we have today is one of two things. A hermetic replay
against a recorded fixture is fast and free and proves nothing about the live
protocol. A live call to OpenAI `gpt-realtime-2.1-mini` proves everything and
costs money, needs credentials, cannot run in CI, and is rate-limited exactly
when a blind-probe fleet wants twenty sessions back to back.

LocalAI closes the gap. It serves an OpenAI-compatible realtime WebSocket at
`ws://localhost:8080/v1/realtime?model=gpt-realtime` — the same URL shape
`agent-cli` already builds for `api.openai.com`, against a `--base-url` flag
that already exists.

| Tier | Endpoint | Cost | May gate |
|---|---|---|---|
| T1 replay | recorded fixtures | free | wire-level and transport behaviour |
| T2 LocalAI | local WebSocket | free | audio round trip, multi-turn, VAD and barge-in, function calling |
| T3 OpenAI | `gpt-realtime-2.1-mini` | metered | everything T2 gates, plus whatever T2 provably cannot serve |

**T2's boundary is measured, not assumed.** LocalAI's realtime pipeline is
composed (`vad → transcription → llm → tts`), so image content parts may or may
not reach the LLM even when the configured LLM is itself vision-capable.
`s2s-lai-local-tier-conformance` answers that empirically and writes the result
into `docs/architecture/s2s-local-tier-conformance.md`. Until that table exists,
no lane may claim a milestone is gated locally. **T2 supplements the acceptance
gate; it never retires it.** The blind-probe acceptance run in §0 is still a T3
run.

### 9.2 Licences — the one rule that can invalidate a diff

| Upstream | Licence | What you may do |
|---|---|---|
| `github.com/mudler/LocalAI` | MIT | run it, depend on it |
| `github.com/WqyJh/go-openai-realtime` | MIT | import as a normal Go module |
| `github.com/gen2brain/malgo` | Unlicense | already our audio dependency |
| **`github.com/localai-org/localai-realtime-demo`** | **none — the API reports `license: null`** | **read it; imitate its design; copy nothing** |

The demo repository has no licence file, so default copyright applies and all
rights are reserved. You may read it and imitate its *design* — the arrangement
of concerns, the stage vocabulary, the fact that a supervisor retries a list of
endpoints — because architecture is not the copyrightable part. You may **not**
copy, paste, vendor, `go get`, or translate line-by-line any of its code, YAML,
comments or test data. A diff containing a block recognisable as lifted from
that repository has failed regardless of whether its tests pass.

Record the licence of every third-party module you add, one line each, in your
PR description.

### 9.3 Surfaces the addendum touches

| Path | Owner |
|---|---|
| `deploy/localai/**` | `s2s-lai-realtime-server-fixture` (TTS model config: `s2s-lai-tts-gguf-format-check`) |
| `go-llm-gateway/pkg/testing/localai/**` | `s2s-lai-realtime-server-fixture` |
| `go-llm-gateway` LocalAI provider + its one registration site | `s2s-lai-gateway-localai-provider` |
| `test/localai/**`, `docs/architecture/s2s-local-tier-conformance.md` | `s2s-lai-local-tier-conformance` |
| `test/probe/localtier/**` | `s2s-lai-blind-probe-local-tier` |
| `docs/architecture/s2s-tts-pinning.md` | `s2s-lai-tts-gguf-format-check` |

Two cross-batch collisions are live and are handled by contention notes, not by
edges. `agent-cli/internal/services/session.go` belongs to
`s2s-b0-realtime-model-allowlist`; no `s2s-lai-*` lane may edit it, and the
gateway provider lane lands its unit-level work and reports the end-to-end item
as blocked if the allowlist has not generalised by its final rebase. Corpus
generation belongs to `s2s-b2-audio-corpus-generator`;
`s2s-lai-tts-gguf-format-check` establishes and pins the facts it depends on and
posts them, but regenerates nothing.

### 9.4 Upstream change to be aware of

LocalAI PR #10316 migrated the `qwen3-tts-cpp` backend to `qwentts.cpp` **and
changed the GGUF format**, telling users to reinstall the model from the gallery
when upgrading. Our corpus artifact is
`C:/Users/andre/.mangaka/models/qwen3-tts-0.6b-f16.gguf`, and the locked decision
for this program is that all test audio comes from local `qwen3-tts.cpp` only.
Corpus audio is the input side of nearly every test here, so if it changes shape
underneath us the failures surface in lanes with nothing to do with TTS.
`s2s-lai-tts-gguf-format-check` resolves and pins it.

