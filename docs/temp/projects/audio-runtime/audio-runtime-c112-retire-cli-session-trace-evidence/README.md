# C112 session-trace evidence

This is the admitted `audio-runtime` evidence for
`audio-runtime-c112-retire-cli-session-trace-evidence`. The final implementation
checkpoint is `77de2f9cd7e3ea5496537f96cde7203249250e6f` on branch
`codex/audio-runtime-c112-retire-cli-session-trace-evidence`.

## Admission, ancestry, and census

- `project-control.py verify-work --type task --name audio-runtime-c112-retire-cli-session-trace-evidence` returns the admitted `audio-runtime` task.
- Accepted planning baseline: `d4766c3dbbf2c198142047ead4449d58dd47d485`.
- Current fetched `origin/main`: `09c70f51243caeaf1184c4806b99bbf7749e3044`; it is an ancestor of the final candidate.
- Required startup ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- The current mainline was integrated by merge commit `97ec64687135f12f915096080f5a8c4af901084f`, preserving the predecessor and C79 merge ancestry.
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
The focused regression and its mutation control cover both behaviors.

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

Final runner result from the committed source used artifact
`artifacts/yui` SHA-256 `e2d1d5fbd1398c3955682fefd82397a159e5042a14d8827a01992835fb6c093e`:

- Tool replay: exit 0, no timeout/survivors, 27 timeline events/20 runtime events, provider PCM 4,800 bytes (`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`), rendered PCM 3,200 bytes (`7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`), speaker trace 4,844 bytes (`305d40c0fa1b687133be6a7841654dcbe89310b7b7f9800cb624c25bcffd880c`), and `PROBE_TOOL_MARKER_9182` plus `strict replay continuation`.
- Interruption replay: exit 0, no timeout/survivors, 20 timeline events/15 runtime events, provider PCM 3,840 bytes (`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`), rendered PCM 3,360 bytes (`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`), speaker trace 3,884 bytes (`001ff24159be9c44e5ba33e39c15cea64818fb488b14b860b623fe95c7a4f2ca`), and replay-complete terminal evidence.
- Fixtures: tool `38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169`; interruption `154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`.

This is credential-free software/file replay evidence. Native Windows hardware,
physical devices, and physical/acoustic claims are OUT OF SCOPE and are never
represented as PASS.

## Handoff

The prior review rejection is preserved in `candidate-evidence.json`. It required
the empty-bundle lifecycle repair, service-owned observer boundary, mandated
verifier/runner/external-consumer paths, fresh final-head evidence, and exact
provenance. Those repairs are complete. Focused causal tests, accumulated
replay regressions, formatting, vet, Wire, architecture-size, coverage
registration, pinned lint, pinned staticcheck, and `git diff --check` pass.

The candidate is ready for the Script CI gate. Script CI and independent review
remain external gates; this implementation does not poll CI or self-review, and
green local checks do not claim CI acceptance.
