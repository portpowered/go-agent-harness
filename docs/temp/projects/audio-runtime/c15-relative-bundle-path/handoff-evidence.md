# C15 relative recording-bundle path handoff

Implementation commit: `bc48f8175ea0c13a37ec73447b1d522e38019e91`

Static-rejection repair commit: `fe9a5c40` (`test: use supported cwd isolation in replay regressions`).

The final candidate source revision is the commit containing this ledger; the
repair checkpoint is its immediate parent.

## Identity and ancestry

- Admitted project/task: `audio-runtime` / `audio-runtime-c15-relative-bundle-path`.
- Branch: `codex/audio-runtime-c15-relative-bundle-path`.
- Current `origin/main`: `f8e0863222da1bdbf296e2220fcbc081461cc877`.
- Required startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21` and baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` are ancestors.
- `prd.json.branchName` matches the isolated branch. The worktree was clean at
  `fe9a5c40` before this ledger update; the final ledger commit remains on the
  same branch and contains only this owned evidence file.

## Rejected-head repair

PR #407 at head `6915824da12971604a4255329c680e84a6e792a4` was rejected by the
canonical task inbox for `CI (static)`. The completed job
`102338796355` in run `34311456260` passed formatting, Wire, architecture/size,
vet and staticcheck, then failed pinned golangci-lint at
`agent-cli/internal/services/internal/replay/service_test.go:470:19`:
`forbidigo: use of os.Getwd forbidden because inject the working directory
through a host boundary`.

The repair replaces `os.Getwd`/`os.Chdir` setup in both newly added
cwd-sensitive replay tests with serial `testing.T.Chdir`, which owns cleanup and
is the repository-supported process-cwd test boundary. The runtime-plan oracle
also now expects the API's absolute lexical artifact path rather than applying
`EvalSymlinks` to the expected value; containment still uses the resolved root
inside production validation. No production security check or acceptance
assertion was weakened.

Repair validation: focused normal and race tests for
`go-agent-runtime/services/replay/internal/plan` and
`agent-cli/internal/services/internal/replay` pass; `rtk make lint` reports 0
issues in all 15 modules; `rtk make architecture-check size-check wire-check`
passes with 181 packages, 1859 files and 27061 functions, with no generated
Wire diff; the accumulated `COUNT=1 YUI_AUDIO_STRESS=1` normal session
regression script passes. The candidate source at the repair checkpoint is
`fe9a5c40`.

## Causal proof

The immutable baseline source is `f8e0863222da1bdbf296e2220fcbc081461cc877`; its executable is SHA-256 `b1010453c36e82bf46e645e1b1929b1e0798156ba54deea3254a9890b578c1af`. The baseline command was:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/c15-relative-bundle-path/public_path_probe.py baseline --binary /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/docs/temp/probes/audio-runtime-c11-hermetic-profile-vertical-probe-stage4/artifact-0 --evidence docs/temp/projects/audio-runtime/c15-relative-bundle-path/evidence/baseline-cross --timeout 30
```

The private-copy report passed all 20 assertions: relative strict bundle paths reproduce exit 1 with `client.transcript.jsonl` resolving outside the recording directory; absolute strict controls exit 0 with `18` wire events, `1` tool call and `strict replay continuation`; the independent provider route passes; and missing, tampered, symlink, nonregular, parent-symlink-escape and manifest-traversal controls fail without `Replay verified`.

The repair canonicalizes `directory` with `filepath.Abs` before `EvalSymlinks`, putting it in the same absolute coordinate system as the already-absolute artifact candidate. Lexical traversal, resolved-parent containment, artifact type, manifest, hash, raw-capture and cancellation checks are unchanged.

## Candidate proof

Candidate build recipe: `rtk proxy go build -o /tmp/audio-runtime-c15-candidate-final ./agent-cli/cmd/yui`.

Candidate executable SHA-256: `c478e11deb528ae3b049838568e706d0e10a6a2e154482bca2b2f112c75235be`.

```text
rtk proxy python3 docs/temp/projects/audio-runtime/c15-relative-bundle-path/public_path_probe.py candidate --binary /tmp/audio-runtime-c15-candidate-final --evidence docs/temp/projects/audio-runtime/c15-relative-bundle-path/evidence/candidate-final --timeout 30
```

The candidate report passed all 20 cases. Every process records expanded argv, cwd, stdout, stderr, exit code, duration and timeout state; each has a 30-second process-group deadline and private fixture copy. The provider-only route reaches `PROBE_TOOL_MARKER_9182`, strict continuation and terminal evidence. This is credential-free software/file-sink evidence, not physical-device or acoustic proof.

## Gates and accumulated regressions

- `rtk proxy go test ./go-agent-runtime/services/replay/internal/plan ./agent-cli/internal/services/internal/replay -count=1 -timeout=60s`: pass.
- `rtk proxy go test -race ./go-agent-runtime/services/replay/internal/plan ./agent-cli/internal/services/internal/replay -count=1 -timeout=60s`: pass.
- `rtk make architecture-check size-check wire-check`: pass; `181` packages, `1859` files, `27063` functions; Wire regeneration produced no tracked changes.
- `COUNT=1 YUI_AUDIO_STRESS=1 rtk proxy bash scripts/test-session-ci-regressions.sh normal`: pass. This includes `TestSessionCommandAudioInterruptOrdering` with all three subtests, high-rate tool/audio trials, duplex PCM/transcript negative controls, shipped replay, and gateway composition. The existing interruption fixture retains its exact healthy 2400-byte tail oracle SHA-256 `16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf` and 15-event replay contract through its assertions.
- `rtk proxy python3 -m py_compile .../public_path_probe.py` and `rtk git diff --check`: pass.

The broad script CI gate has not been run or polled by this executor. Independent review, guarded merge, post-merge vertical validation and project acceptance remain external stages. Submit this same commit to SCRIPT CI; retain the task for any exact terminal rejection.
