# C120 pre-mutation census

Recorded before production implementation changes on 2026-09-12 in the
admitted worktree.

## Admission and ancestry

- Factory root: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness`
- Project: `audio-runtime`
- Contract: `audio-runtime-v1`
- Factory session: `~default`
- Admission command: `project-control.py verify-work --type task --name audio-runtime-c120-retire-cli-session-duration-terminal-boundary`
- Admission result: `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c120-retire-cli-session-duration-terminal-boundary"}`
- Worktree branch: `codex/audio-runtime-c120-retire-cli-session-duration-terminal-boundary`
- `prd.json.branchName`: `codex/audio-runtime-c120-retire-cli-session-duration-terminal-boundary`
- Accepted task base: `3963bc3566da24f8214634c17a9d0f79a6724171`
- Required startup integration: `8bdafc7f947a3a2c9856220abdc539437035bd21` is an ancestor of the task base.
- Required baseline: `3963bc3566da24f8214634c17a9d0f79a6724171` is an ancestor of the task base.
- Fresh fetch completed before this census: `origin/main=b7d25ca6f0e9b94c62b193059160dfbf446ef1d6`.
- The branch was clean before this evidence file was added. The fetched main is four commits ahead and is not merged in this checkpoint; no host checkout was reset or merged.

## Immutable target and callers

At the accepted task base:

- `agent-cli/internal/services/internal/agentruntime/session_duration_terminal.go` is exactly 131 lines.
- Its SHA-256 is `82917c1a30ceed01da41109e368c2c6851b2ed6c5e5b689959f9d8faa5f88b88`.
- `agent-cli/internal/services/internal/agentruntime/session_duration_loop.go` is 534 lines and SHA-256 `2bf32471d10af9117600c5006d453fb12beb6a134e0461f0e6b895005270591f`.
- The target file's exact symbol census found no callers outside the duration loop:
  - `newSessionDurationTerminalState`: loop line 106.
  - `writeObservedProviderTerminal`: loop line 140 and the loop's buffered flush path.
  - `sessionDurationLifecycleError`: loop line 155.
  - `sessionTransportError`: loop lines 156 and 164.
  - `writeMaxDurationTerminal`: loop line 170.
  - `sessionDurationTerminalState` and its methods: loop message processing and buffered/straggler drain helpers.
- The loop is an explicit byte-for-byte exclusion. All current duration callers remain outside the mutation set.
- A repository-wide exact-name search found no exported, reflection, plugin, or generated reference to the target symbols. Static search cannot rule out arbitrary runtime behavior outside Go symbol references; the symbols are unexported and no dynamic reference was found in the admitted source.

## Ownership exclusions

These paths were recorded as active peer or predecessor ownership and are not
part of C120's mutation set:

- C79: `scripts/wire-packages.txt`, `docs/architecture/architecture-size-baseline.json`, and its response-lifecycle CLI paths.
- C104 predecessor: `agent-cli/internal/services/internal/agentruntime/session_cancellation.go`, `session_cancellation_test.go`, `session_finalizer.go`, `session_termination.go`, `session_termination_test.go`, `go-agent-runtime/services/sessionfinalization/`, its coverage manifests, and its evidence/external-consumer files.
- C107: `docs/temp/projects/audio-runtime/audio-runtime-c107-characterize-post-wave-cli-ownership/**`.
- C109: `docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/` is not owned by C120; the active C109 evidence directory is disjoint.
- C110: `session_instructions.go`, its C110 test, `go-agent-runtime/services/sessioninstructions/`, its coverage manifest, evidence, and its deferred shared registries.
- C115: filesystem tool paths, `go-agent-runtime/services/audiocodec/`, coverage, and evidence.
- C116: RTC track paths, `agent-cli/internal/wire/rtc_runtime.go`, `go-agent-runtime/services/rtctransport/`, coverage, and evidence.
- C117: livehost/trace publication paths, C108 verification/provenance/SHA files, and evidence.
- C118: `session_diagnostics_terminal.go`, its test, `go-agent-runtime/services/sessionterminal/`, coverage, evidence, compatibility caller paths, and deferred shared registries.
- C119: `session_diagnostics_failure.go`, `session_failure_projection_adapter.go`, its test, `go-agent-runtime/services/sessionfailure/`, coverage, and evidence.

C120 owns only the target terminal file replacement, the new
`go-agent-runtime/services/sessionduration/` service and dedicated Wire,
matching coverage manifests, and this task's evidence directory. The duration
loop and all listed peer paths remain unchanged.

## Public probe inputs

The immutable C38 audio/tool continuation fixture is used as a source-pinned
offline input, not as a second project or acceptance authority:

- `c16-audio-tool.session.json` SHA-256:
  `38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169`.
- `c16-interruption.session.json` SHA-256:
  `154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`.

The task-owned `run.py` builds no network session and removes provider
credentials from its child environment. It launches the exact yui artifact in
its own process group, caps captured output, applies a child and aggregate
deadline, and checks for zero remaining group members. It records terminal
manifest metadata and PCM/tool artifacts under ignored `runs/` output. The
four cases are max-duration replay, provider-close replay, a loop-close
negative control, and the healthy audio/tool continuation.

## Prior review and project boundary

Canonical Factory work listing for the C120 name contained only the idea, plan,
and `work-task-283` task (`init`); no `work-review` record or prior C120 finding
was present. The admitted manifest is the only project manifest used. No second
project, acceptance waiver, or completion claim is authorized.
