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

The repaired head has not been resubmitted or polled for terminal CI. The
unrelated hermetic room and coverage provider-burst failures visible later in
the same still-running GitHub run are outside this task's owned paths and are
not claimed fixed here. The next action is to commit this evidence update,
push the same PR #405 head, and return `ACCEPTED` to the script-owned CI gate;
any exact same-task rejection remains with this executor.

Rollback for the static repair is `git revert 90f4417`.
