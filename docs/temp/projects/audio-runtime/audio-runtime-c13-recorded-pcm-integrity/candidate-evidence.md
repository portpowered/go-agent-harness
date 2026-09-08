# C13 committed candidate evidence

This ledger records the committed C13 candidate after the preserved baseline
comparison. The public probe always copies the source fixture into private
`untouched/` and `mutated/` directories; it never edits the source fixture.

- Task: `audio-runtime-c13-recorded-pcm-integrity`
- Admission: `project-control.py verify-work --type task --name audio-runtime-c13-recorded-pcm-integrity` returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c13-recorded-pcm-integrity"}`.
- Branch: `codex/audio-runtime-c13-recorded-pcm-integrity`
- Committed candidate: `6a7e3199705671541b80b895820ffdca7be7689a`
- `prd.json.branchName`: `codex/audio-runtime-c13-recorded-pcm-integrity`
- `origin/main` at baseline and candidate ancestry: `668f2d8816beaa078d058b3f0bcc59600b71a023`
- Preserved startup integration ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- Preserved original baseline ancestor: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`
- Candidate executable: `/tmp/audio-runtime-c13-candidate-final-yui`
- Candidate executable SHA256: `93e024c13038aac226cfc5e2936c8beb07106d15b799570f98eeb5584f659141`
- Public probe: `public_integrity_probe.py candidate --binary /tmp/audio-runtime-c13-candidate-final-yui --evidence /tmp/audio-runtime-c13-candidate-final-driver`
- Probe run: `/private/tmp/audio-runtime-c13-candidate-final-driver/candidate-98694-1788895616`
- Source fixture: `$FACTORY_ROOT/docs/temp/probes/audio-runtime-c12-interruption-replay-vertical-probe/evidence/runs/interruption/bundle`
- Source manifest SHA256: `0b874fb3f6c996814c49f8d15c411db32d7810ed44105871ba3aca5b8b61e39e`
- Source provider SHA256: `154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`
- Declared `audio/out-000.pcm` SHA256: `6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`
- Mutated same-size PCM SHA256: `039aade3a3a49c85ea2801b8257b3ace6b533916aa9d240c95ab8d6f7a282dc9`
- PCM size remained `3840` bytes; manifest and provider hashes remained unchanged.

Public candidate result:

- `session replay <bundle>`: untouched exit `0` in `0.539s`, no timeout, `Replay verified`; mutated exit `1` in `0.024s`, no timeout, artifact-specific digest mismatch for `audio/out-000.pcm`.
- `session --replay <bundle> --audio-out <sink> --max-duration 60s`: untouched exit `0` in `0.046s`, no timeout, `classification=replay_complete` and `[session replay complete]`; mutated exit `1` in `0.023s`, no timeout, artifact-specific digest mismatch for `audio/out-000.pcm`.

Implementation and regression evidence:

- The runtime replay directory admission now validates every declared manifest artifact, including recorded PCM, with safe relative paths, non-symlink regular-file checks, containment checks, and bounded context-aware SHA-256 streaming. Existing provider artifact selection remains the returned protocol capture boundary.
- Internal replay validates a root `manifest.json` through the shared runtime admission boundary before selecting `timeline.jsonl` or `audio-trace`. Raw captures and standalone trace directories without a root manifest retain their existing legacy replay scope.
- `go test ./go-agent-runtime/services/replay/... ./agent-cli/internal/services/internal/replay/... ./agent-cli/internal/services/replay/... -count=1 -timeout=60s`: passed.
- `go test -race ./go-agent-runtime/services/replay/... ./agent-cli/internal/services/internal/replay/... ./agent-cli/internal/services/replay/... -count=1 -timeout=60s`: passed.
- `go test -race ./agent-cli/test/integration -run TestSessionRecordedPCMIntegrity -count=1 -timeout=60s`: passed.
- Accumulated C12 race regressions and replay/live plan checks: passed.
- `make architecture-check size-check`: passed (`181 package(s), 1859 file(s), 27036 function(s) checked`).
- `make wire-check`: passed with generated wire output clean.
- `git diff --check`: passed; committed worktree was clean before the final candidate build.

The candidate is ready for the script CI gate. CI status has not been polled
and is not claimed here. The probe does not establish device consumption,
acoustic output, or the separate `--audio-out` sink as a recorded artifact.
The manifest remains self-unhashed by the existing transcript contract.

Rollback: `git revert 6a7e3199705671541b80b895820ffdca7be7689a`.

## C13 static-gate repair checkpoint

The first submitted head `05f2551ca878377134f75e243ea5131a82d6e16f` was
returned by the Factory after PR #405 run `34269150751`. The completed static
job `102206009938` passed formatting, Wire, architecture/size, vet and
staticcheck, then failed pinned golangci-lint `2.9.0` on two errorlint findings
in the new adapter: both wrapped causes used `%v` instead of `%w` at
`recording_directory.go:25` and `:31`. After those were corrected, the full
local pinned lint exposed one additional same-candidate `mnd` finding in
`directory.go:277` (`32*1024`); the buffer size was named without changing
streaming behavior. The repair is committed as
`90f4417dbfff4efaf640be67ba8076b0ba8a0538` (short `90f4417`).

- Current source: `90f4417d`; branch remains `codex/audio-runtime-c13-recorded-pcm-integrity` and `prd.json.branchName` matches.
- `origin/main`: `668f2d8816beaa078d058b3f0bcc59600b71a023`; startup and original baseline ancestry remain satisfied.
- Rebuilt candidate: `/tmp/audio-runtime-c13-candidate-lint-repaired-yui`.
- Candidate SHA256: `6cde9a9e9ca4fae5e63b914258643c6cda8e6f8ba0809646249164c3424aaef0`.
- Public probe: `public_integrity_probe.py candidate --binary /tmp/audio-runtime-c13-candidate-lint-repaired-yui --evidence /tmp/audio-runtime-c13-repaired-driver`.
- Probe run: `/private/tmp/audio-runtime-c13-repaired-driver/candidate-3599-1788896496`.
- Untouched `session replay` and `session --replay --audio-out` exited `0`.
- Same-size one-byte PCM mutations exited `1` on both routes in `0.040s` and `0.022s`, each naming `audio/out-000.pcm` and the expected/actual digest mismatch.
- Post-repair focused replay packages and `TestSessionRecordedPCMIntegrity` passed normal and race; six accumulated C12 replay/continuation/recording regressions passed normal.
- `make lint`, `make staticcheck`, `make vet`, `make fmt`, `make wire-check`, `make architecture-check`, `make size-check`, and `git diff --check` pass. Architecture/size remains `181 package(s), 1859 file(s), 27036 function(s) checked`.

At this checkpoint the repaired head has not been resubmitted or polled for terminal CI. The
unrelated hermetic room and coverage provider-burst failures visible later in
the same still-running GitHub run are outside this task's owned paths and are
not claimed fixed here. This evidence update is committed locally; the next
action is to push the same PR #405 head and return `ACCEPTED` to the
script-owned CI gate;
any exact same-task rejection remains with this executor.

Rollback for the static repair is `git revert 90f4417`.

## C13 current-head CI rejection reconciliation

The next submitted head was `4766485ecebb270a99adefd434964d5cfe54c40c`.
The raw run record and complete failed-job logs were saved from GitHub Actions
run `34270641520` (completed `failure` at `2026-09-08T19:52:50Z`):

- `CI (hermetic)`, job `102211060946`, failed only at
  `go-audio/pkg/mixer.TestMixerPreservesShortResponseTailAndEpochs` under
  `CGO_ENABLED=0`, `tags=nomicrophone`. The assertion observed a new epoch
  frame containing source epoch data and expected the source epoch to be
  excluded from the mix.
- `CI (integration)`, job `102211061026`, failed only at
  `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test48_matched_healthy_control/provider_burst`.
  The remote playback did not reach its final PCM marker before the existing
  30-second scenario deadline.
- The other seven required jobs in the same run passed. The failed test paths
  are absent from the C13 diff against `origin/main`; C13 changes only replay
  admission, replay service delegation, the owned integrity regression, and
  its evidence. No in-scope C13 defect was reproduced by these failures.

Bounded causal rechecks on the exact candidate tree passed:

- `CGO_ENABLED=0 go test ./go-audio/pkg/mixer -tags=nomicrophone -run '^TestMixerPreservesShortResponseTailAndEpochs$' -count=3 -timeout=60s`
  passed three times.
- `go test ./agent-cli/test/integration -run '^TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test48_matched_healthy_control/provider_burst$' -count=1 -timeout=90s`
  passed in `17.834s`; no C13-owned source or test change was made for this
  external remote-device timing failure.
- C13 normal and race replay packages, `TestSessionRecordedPCMIntegrity`
  under race, and replay `Interruption|Cancel` controls all passed.
- A fresh candidate build from `4766485e` retained SHA256
  `6cde9a9e9ca4fae5e63b914258643c6cda8e6f8ba0809646249164c3424aaef0`.
  The fresh private public probe run
  `/private/tmp/audio-runtime-c13-ci-retry-driver/candidate-8020-1788897666`
  passed untouched `session replay` and `session --replay` (exit `0`) and
  rejected the same-size one-byte `audio/out-000.pcm` mutation on both routes
  (exit `1`, naming the expected and actual artifact digests).

This is a fresh evidence checkpoint resolving the returned checks as an
out-of-scope hermetic mixer assertion and a non-reproduced remote-device timing
failure, not an unchanged implementation resubmission. The candidate remains
ready for the script-owned CI gate; no CI success, independent review, merge,
vertical acceptance, device consumption, acoustic output, or project completion
is claimed.

## C13 review repair: reject dangling root manifests

The canonical review rejection for head `22bf02febbbd8f4ea92aeaa021d41c75f09e1978`
identified a public integrity bypass: `os.Stat` treated a dangling
`manifest.json` symlink as absent, allowing `session replay` to use the
standalone trace path and report `Replay verified`. The new public regression
reproduced that exact false success before the repair:
`go test ./agent-cli/test/integration -run '^TestSessionRecordedPCMIntegrity$'
-count=1 -timeout=60s` failed with `dangling manifest unexpectedly succeeded`
and the `Replay verified` marker.

Repair commit `6b22eba5f1a3937205abd5a7e628f6bcf4072dc0` changes the CLI root
manifest detector to `os.Lstat`, explicitly rejects symlink and non-regular
manifest entries, and still treats a truly absent manifest as the documented
legacy standalone-trace case. The focused unit controls cover both dangling
symlink and directory manifests plus manifestless trace preparation. The public
`TestSessionRecordedPCMIntegrity` control exercises both `session replay` and
`session --replay`; both reject the dangling manifest without a verification or
completion marker.

- Focused normal replay packages and runtime `Interruption|Cancel` controls pass:
  `go test ./agent-cli/internal/services/internal/replay -count=1 -timeout=60s`,
  `go test ./go-agent-runtime/services/replay/... ./agent-cli/internal/services/replay/... -count=1 -timeout=60s`,
  and `go test ./go-agent-runtime/services/replay/internal/plan -run 'Interruption|Cancel' -count=3 -timeout=60s`.
- Focused race controls pass:
  `go test -race ./go-agent-runtime/services/replay/... ./agent-cli/internal/services/internal/replay/... ./agent-cli/internal/services/replay/... -count=1 -timeout=60s`
  and `go test -race ./agent-cli/test/integration -run '^TestSessionRecordedPCMIntegrity$' -count=1 -timeout=60s`.
- Exact committed candidate source is `6b22eba`; rebuilt `yui` is
  `/tmp/audio-runtime-c13-candidate-manifest-repaired-yui` with SHA256
  `485a441055b9245fd295386c45bb1550df233adde8a3149d036c256540a36f1d`.
- Fresh bounded exact-binary probe is
  `/private/tmp/audio-runtime-c13-manifest-repair-driver/candidate-12547-1788899352`.
  Untouched `session replay` and `session --replay` exited `0`; same-size
  one-byte `audio/out-000.pcm` mutations exited `1` on both routes with
  artifact-specific expected/actual digest diagnostics. PCM stayed `3840`
  bytes; manifest and provider hashes were unchanged.

No CI terminal result, independent review, merge, vertical acceptance, device
consumption, acoustic output or project acceptance is claimed. The next action
is to push this repair on the same PR #405 and return `ACCEPTED` to the
script-owned CI gate without polling it; any exact same-task CI rejection
remains with this executor.

## C13 final focused gate checkpoint

After the repair and evidence checkpoint, current-source focused gates also
pass: `make fmt`, pinned `make lint` (0 issues in all 15 modules), pinned
`make staticcheck`, `make vet`, `make wire-check`, `make architecture-check
size-check` (`181` packages, `1859` files, `27042` functions), and `git diff
--check`. Wire generation is unchanged and the worktree is clean. The pushed
PR head and local HEAD are both `161f75dba70ba19c3f452fbe0c0d94ce7d70d02f`;
`origin/main` remains `668f2d8816beaa078d058b3f0bcc59600b71a023`, with the
startup and original baseline ancestors preserved. Admission recheck remains
`admitted` for `audio-runtime-c13-recorded-pcm-integrity`.
