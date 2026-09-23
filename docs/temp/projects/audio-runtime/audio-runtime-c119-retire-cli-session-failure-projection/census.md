# C119 immutable admission and caller census

Captured before production mutation on 2026-09-13 (America/Los_Angeles).

## Admission and ancestry

- Project: `audio-runtime`; no second project and no acceptance waiver.
- Factory session: `~default`; server: `$FACTORY_SERVER_URL` (`http://127.0.0.1:7439`).
- Admission command: `rtk proxy python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c119-retire-cli-session-failure-projection --root "$FACTORY_ROOT"`.
- Admission result: `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c119-retire-cli-session-failure-projection"}`.
- Branch: `codex/audio-runtime-c119-retire-cli-session-failure-projection`.
- `prd.branchName`: exact match.
- `HEAD`: `3963bc3566da24f8214634c17a9d0f79a6724171`.
- `origin/main`: `3963bc3566da24f8214634c17a9d0f79a6724171` after `rtk git fetch origin main`.
- Startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21` is an ancestor of `HEAD`; current baseline is also an ancestor.
- Worktree was clean before this owned evidence file. The running host checkout was not merged, reset, or otherwise mutated.

## Immutable source

- Source: `agent-cli/internal/services/internal/agentruntime/session_diagnostics_failure.go` at accepted `origin/main`.
- Physical source count: `246` lines.
- SHA-256: `c6531d0c2a0f3556c2bc478f911a1687242456fd6ba920f370aa51eb0e7ecfd6`.
- Baseline source contains normalization/defaults, cancellation and non-terminal exclusion, close handling, projections, unsupported-tool diagnostics, snapshot/clear, and first-publication callbacks. Tests and generated artifacts are not counted as retirement.

## Static symbol and caller census

The following direct callers were found with `git grep` against the baseline. Caller files are excluded from this task and must remain byte-for-byte unchanged.

| Symbol | Direct baseline callers |
| --- | --- |
| `failureSnapshot` | `session_cancellation.go:73`; `session_diagnostics_terminal.go:35` |
| `clearFailure` | `session_diagnostics_terminal.go:205` |
| `captureFailureFromError` | `session_diagnostics_observation.go:291` |
| `captureFailureFromClose` | `session_diagnostics_observation.go:296` |
| `factsFromSessionRunError` | `session_diagnostics_terminal.go:57`; `session_room_terminal.go:87` |
| `acceptFailureObservation` | `session_room_terminal.go:101` |
| `unresolvedToolResultFailureFacts` | `session_diagnostics_terminal.go:42,61` |
| `imageContinuationFailureFacts` | `session_diagnostics_terminal.go:45,65` |
| `toolContinuationFailureFacts` | `session_diagnostics_terminal.go:48,63` |
| `scheduledAudioFailureFacts` | `session_diagnostics_terminal.go:51` |
| `emitToolCallRecord` | `session_diagnostics_observation.go:227` |
| `deriveOutputState` | `session_room_coordinator.go:696`; `session_room_lifecycle.go:425,440`; `session_room_run.go:638`; `session_diagnostics_terminal.go:79` |

Additional direct state consumers are `session_diagnostics.go` (observer field and mutex), `session_liveness.go` (direct liveness-failure latch under the same mutex), `session_room_terminal.go` (fallback facts and terminal conversion), and `session_diagnostics_test.go` (non-terminal assertion). These are not C119-owned callers.

### Dynamic/interface uncertainty

- `messages.ErrorValue.Err` is an interface-bearing original error; the extracted service must retain its identity for `errors.Is`/`errors.As`, while the adapter may only convert values.
- `engine.StreamDeltaError` is recovered through `errors.As`; the stream loop delivers it through the existing observation path rather than a statically typed C119 call.
- `failureObserver`, `terminalObserver`, and diagnostic sinks are callback/interface edges. The service must publish its immutable accepted snapshot before invoking those callbacks; adapter reservation is only compatibility state.
- Liveness failure facts are directly constructed in the excluded liveness caller and therefore remain a compatibility case with no C119 policy duplication.

## Ownership and exclusions

- C107 characterization is non-authorizing evidence only; it does not own C119 mutation.
- C79 exclusively leases `scripts/wire-packages.txt` and `docs/architecture/architecture-size-baseline.json` until reviewed guarded merge and explicit release.
- C118 owns `session_diagnostics_terminal.go`, its test, `services/sessionterminal/`, and its coverage manifest; C119 does not edit them.
- C111 owns `session_tool_lifecycle.go`, its test, `services/sessioncontinuation/`, and its coverage manifest; C119 does not edit them.
- C112 owns `trace.go`, its test, `services/sessiontrace/`, and its coverage manifest; C119 does not edit them.
- C120 owns `session_duration_terminal.go`, its adapter/test, `services/sessionduration/`, and its coverage manifest; C119 does not edit them.
- Other active retirement identities (including C103, C104, C108, C109, C110, C114, C115, and C117) retain their declared paths. No C119 service or adapter path existed before this checkpoint.

Pre-mutation SHA-256 fingerprints for excluded caller/shared paths:

```text
0584e3acc16e29b8fb79ece8a19199f0f8690de39099af922f08781edd957eb8  agent-cli/internal/services/internal/agentruntime/session_diagnostics.go
400eba47b433673f95929d56d68f2d610cd145eab99d0712f571f1125a6c0281  agent-cli/internal/services/internal/agentruntime/session_diagnostics_observation.go
4608c5c35277c97375c89e989e8a9c88dc6376968239ee66d713a0ee3f65b369  agent-cli/internal/services/internal/agentruntime/session_diagnostics_terminal.go
50c9c02f61a822c5e20c97dad59b8627c171f9ba85a85d2bb3d496fb2b760b57  agent-cli/internal/services/internal/agentruntime/session_room_terminal.go
d9de111ed44d79045cdc492a7309314cad81c7e478ee8a2fea7a1c9755d2eb17  agent-cli/internal/services/internal/agentruntime/session_liveness.go
bdb7e50876bd8d9b67fe04a736da9a91ce3ea2bbd10024ebf27d0f6bbbcd9edd  agent-cli/internal/services/internal/agentruntime/session_cancellation.go
11fae882fa7232b6c6b797d4bdbf45428e4e49efba7d74dd4e43609dcc59fa92  agent-cli/internal/services/internal/agentruntime/session_room_coordinator.go
33acf07ea4f4a71fcf332674efbcecf70a0a01c067c626c5b85fae625e8903ec  agent-cli/internal/services/internal/agentruntime/session_room_lifecycle.go
2ac0ff50ee59a1ec8f04682e04d876d270146ecda8245c5d14d28a5f7120e8d6  agent-cli/internal/services/internal/agentruntime/session_room_run.go
48000dcd73b85ebbe93fd40a3996529878a99c799b67b0779d60f55dbb43fc1e  agent-cli/internal/services/internal/agentruntime/session_tool_lifecycle.go
3958443ed08ad6a550fd29ce8191c026d1a4017339e00c0b7406e0dcec7de05b  scripts/wire-packages.txt
6d61448b83eca374b324335f6a9e2fac768969d073b1091d17d48d45bf3badd1  docs/architecture/architecture-size-baseline.json
```

The fingerprints are recorded from one pre-mutation `shasum` run; later verification will recompute them and fail closed on any mismatch.

## Candidate-independent oracles

- Existing named regressions remain authoritative: non-terminal diagnostic, connect/mid-stream/drain failure exactly-once records, clean close no-record, SIGINT cancellation with an independent failure, and existing room/provider/tool regressions.
- New public-service tests will exercise defaults, cancellation/non-terminal/room cancellation exclusion, provider-close marker, output-state derivation, immutable snapshots, original-error identity, first accepted publication before reentrant callback, synchronized clear, projections, and unsupported-tool diagnostic fields.
- Two causal mutations are required: suppress terminal failure facts (positive oracle must fail) and admit cancellation as failure (cancellation-negative oracle must fail).
