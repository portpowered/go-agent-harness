# C112 session-trace evidence

This is the admitted `audio-runtime` evidence for
`audio-runtime-c112-retire-cli-session-trace-evidence`. The repaired C112
implementation checkpoint is `77de2f9cd7e3ea5496537f96cde7203249250e6`; the
current-main-integrated source/probe checkpoint is
`708013bd419175482fd5aad5d1e83432e8d71b33`; the final atomic-publication repair
checkpoint is `dee72ae047db826a049c41fe5be2410d09135c6e`; and the deterministic
public-boundary evidence-runner checkpoint is
`4d1264c682c046ea69a95075efb3941bd496101c`. These checkpoints follow final
candidate merge `cea43e874a6d2d2eded0ab0f5b19e48dcfd3b983` on branch
`codex/audio-runtime-c112-retire-cli-session-trace-evidence`.

## Admission, ancestry, and census

- `project-control.py verify-work --type task --name audio-runtime-c112-retire-cli-session-trace-evidence` returns the admitted `audio-runtime` task.
- Accepted planning baseline: `d4766c3dbbf2c198142047ead4449d58dd47d485`.
- Current fetched `origin/main`: `bd6a1289218d1bef1a3af36e64e9d4496062416f`; it is an ancestor of the final candidate.
- Required startup ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- The C116 current-main integration was merge commit `708013bd419175482fd5aad5d1e83432e8d71b33`; the newly fetched mainline `bd6a1289218d1bef1a3af36e64e9d4496062416f` was then integrated by final candidate merge `cea43e874a6d2d2eded0ab0f5b19e48dcfd3b983`, preserving the predecessor and C79 ancestry. Checkpoint `dee72ae` adds the service-owned atomic no-replace publication repair; checkpoint `4d1264c` adds the public sessiontrace boundary probe, and the shipped artifact was rebuilt from that checkpoint.
- The immutable pre-extraction `trace.go` is 119 lines with SHA-256 `db9fab41dd02578db5e7b024af869eb762b21ec14c48c64442d4ad9ca3149710`.
- The final CLI seam is 43 lines with SHA-256 `939fa231d9b182536297cf18f923a6f358fec46a7dcbf5ea64bb7db548ed5d75`; 76 lines of CLI lifecycle/orchestration are retired.

## Boundary repair

`agent-cli/internal/services/internal/agentruntime/trace.go` now performs only
request, device-callback, and observation type adaptation. The prior runtime
observer is passed into `go-agent-runtime/services/sessiontrace`; the private
service owns ordered fanout, payload isolation, redaction, preferences, bounded
close, retention, and atomic no-overwrite publication. The old
`session_audio_trace.go` implementation is absent. Device ownership remains in
the device gateway and trace storage remains in `go-audio/pkg/recording`.

`Finish(ctx, "", false)` now returns the staged-path diagnostic even after a
successful close, and `errors.Join` preserves a distinct close-error identity.
Published traces claim the destination and use a platform-native no-replace
rename, so a destination created after the claim cannot be overwritten; the
adversarial concurrent-destination test verifies the sentinel and staged trace
remain intact. The focused regressions and mutation controls cover these
behaviors.

The external consumer is exactly at `external-consumer/`; it imports only the
public sessiontrace contract and generated Wire constructor. It passes:

```text
GOWORK=off go test ./... -count=1
GOWORK=off go build .
```

## Bounded verifier and replay runner

The restored evidence entrypoints are:

```text
python3 .../verify.py --mode provenance
python3 .../verify.py --mode retirement-and-owned-paths
python3 .../verify.py --mode positive-and-three-mutations
python3 .../run.py --case trace-audio-tool-replay --case interruption-replay --child-timeout 60 --aggregate-timeout 180
```

The verifier passes admission, branch/ancestry, ownership, host-neutral imports,
normal/race focused tests, the external consumer, and three causal mutations:
empty unpublished retention, dropped prior-observer wiring, and shared payload
copying. The runner strips credential environment variables, caps output,
reaps the child process group, and checks source-pinned fixture and PCM hashes.
Each shipped replay case also invokes
`external-consumer/traceprobe/main.go` in a separate `GOWORK=off` public-service
probe. That probe exercises the four service taps—microphone pre-gate,
microphone upload, speaker enqueue, and speaker render—plus provider
send/receive/terminal events and serialized redaction checks.

Final runner result from a yui artifact rebuilt at checkpoint
`4d1264c682c046ea69a95075efb3941bd496101c` using `make -C agent-cli build`
passed with `AGENT_MODEL__OPENAI__API_KEY` and
`AGENT_MODEL__GROK__API_KEY` injected into the parent and removed before launch.
The artifact is `artifacts/yui` SHA-256
`1cfadaea74eb80388e2b2a170fb0eddd9de8a611994ac4deb2c6161a602d1bbd`, and run
directory `runs/vertical-20260913T134620Z-59130`:

- Tool replay: exit 0, no timeout/survivors, 29 timeline events/21 runtime events, provider PCM 4,800 bytes (`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`), rendered PCM 3,200 bytes (`7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`), speaker trace 4,844 bytes (`305d40c0fa1b687133be6a7841654dcbe89310b7b7f9800cb624c25bcffd880c`), deterministic microphone input 1,440 bytes (`db9ac5111b2173f5f0e7909539a7dea70134b5736d4f2582c99e25705aab6148`), derived fixture (`62835dcb8270ab1dba865d078e79b13a3c6c3c89454c65f5eb7f5bb42586a714`), microphone pre-gate and speaker-enqueued taps, provider wire append/commit types, and `PROBE_TOOL_MARKER_9182` plus `strict replay continuation`.
- Interruption replay: exit 0, no timeout/survivors, 20 timeline events/15 runtime events, provider PCM 3,840 bytes (`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`), rendered PCM 3,360 bytes (`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`), speaker trace 3,884 bytes (`001ff24159be9c44e5ba33e39c15cea64818fb488b14b860b623fe95c7a4f2ca`), and replay-complete terminal evidence.
- Per-case public-service boundary probe: exit 0 with 9 timeline events/3 runtime events, all four taps (`microphone_pre_gate`, `microphone_uploaded`, `speaker_enqueued`, `speaker_rendered`), provider send/receive/terminal kinds, redacted credential payload/error fields, and no surviving process-group PIDs. The livehost replay remains honest file-replay evidence; it does not expose a physical render callback.
- Both children exited with code 0, no surviving process-group PIDs, and a credential-free environment; the runner removed `AGENT_MODEL__GROK__API_KEY` and `AGENT_MODEL__OPENAI__API_KEY`.
- Fixtures: source tool `38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169`, derived audio tool `62835dcb8270ab1dba865d078e79b13a3c6c3c89454c65f5eb7f5bb42586a714`, and interruption `154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`.

The concurrent run `runs/vertical-20260913T100247Z-94048` is preserved as a
negative diagnostic, not relabeled: interruption replay exited 0 without
survivors but retained only 2,400 provider/rendered bytes across 19 timeline
events (`16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`).
The quiet rerun `runs/vertical-20260913T100404Z-409` and the rebuilt source/probe
run above passed.

This is credential-free software/file replay evidence. Native Windows hardware,
physical devices, and physical/acoustic claims are OUT OF SCOPE and are never
represented as PASS.

## Fresh local gate checkpoint — 2026-09-13T13:46:20Z

On evidence-runner checkpoint `4d1264c682c046ea69a95075efb3941bd496101c`, with
final candidate merge `cea43e874a6d2d2eded0ab0f5b19e48dcfd3b983` and
the required fetched `origin/main` `bd6a1289218d1bef1a3af36e64e9d4496062416f`, the owned
`verify.py --mode all` passed admission, startup/accepted/current-main
ancestry, owned-path scope, sessiontrace normal/race tests, CLI adapter
normal/race tests, the `GOWORK=off` external consumer build/test, and all three
causal mutation controls. The verifier's mutation controls failed for their
intended assertions and were accepted by the fail-closed verifier; the
concurrent-destination regression and Darwin/Windows compile probes also pass.

The accumulated command
`COUNT=1 bash scripts/test-session-ci-regressions.sh all` passed in normal,
coverage, and race modes: CLI interruption, shipped replay/continuation and
duplex controls, simulated-device controls, composed OpenAI tool lifecycle, and
the strict 20-trial high-rate control. `make wire-check` and
`make architecture-size-check` pass at 198 packages, 1,931 files, and 28,679
functions. Full vet, pinned staticcheck, coverage registration, and the scoped
owned-module golangci-lint check also pass. The final owned-path scope check and
`git diff --check` pass. The fresh yui run is
`runs/vertical-20260913T134620Z-59130`; its separate public-service probes
passed all four service taps and provider send/receive/terminal redaction for
both replay cases.

## Handoff

The prior review rejection is preserved in `candidate-evidence.json`. It required
the empty-bundle lifecycle repair, service-owned observer boundary, mandated
verifier/runner/external-consumer paths, fresh final-head evidence, and exact
provenance. The latest independent review also required atomic no-replace
publication, microphone/upload/playback evidence, complete configured-credential
scrubbing, and an artifact rebuilt from the reviewed checkpoint; those repairs
are complete at `dee72ae`, with the public-service boundary probe and refreshed
artifact/run bound to `4d1264c`. Focused causal tests, accumulated replay regressions,
formatting, vet, Wire, architecture-size, coverage registration, scoped pinned
lint for the owned runtime module, pinned staticcheck, and `git diff --check`
pass. The repository-wide lint target still reports five unrelated baseline
C124 `agent-cli` findings outside the admitted C112 paths; those predecessor
changes are preserved and are not relabeled as C112 failures.

The prior Script CI outcomes are retained without relabeling: PR #498 head
`fe10ea3` had green run `34752205668` before this current-main integration, while
the earlier run `34749961374`/job `103704601047` failed only at `CI (hermetic)` on
the C47-owned remote high-rate device playback test
`TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/captured_cadence`
(167,991/174,391 samples retained; 6,400 lost). No C112-owned path or causal
C112 defect was implicated. Changed head `4d1264c682c046ea69a95075efb3941bd496101c`
is ready for the script-owned CI gate; no new CI result is claimed or polled by
the executor.
