# C112 session-trace evidence

The original extraction source checkpoint was
`ecdf54bc138b58b0454240b519cd1038acd2aec6`, based on accepted
`origin/main d4766c3dbbf2c198142047ead4449d58dd47d485`. The immutable source
baseline is the 119-line `trace.go` with SHA-256
`db9fab41dd02578db5e7b024af869eb762b21ec14c48c64442d4ad9ca3149710`; the
candidate adapter is 49 lines with SHA-256
`6abfe4c37125209159d18a28fa9544fbfcb1c7a630a3cf0b193adf6e34aab177`.

See `candidate-evidence.json` for caller inventory, focused/race/consumer and
shipped-YUI results, exact shared-gate findings, and the acceptance boundary.

This admitted evidence directory contains `consumer`, a separate Go module
that imports only the public `go-agent-runtime/services/sessiontrace` contract
and its generated Wire constructor. It has no `agent-cli` or private-runtime
imports.

The consumer test constructs two independent services with deterministic
clocks, records all four PCM edges plus a runtime event, publishes both
bundles, verifies that same-length PCM mutation changes the immutable WAV
identity, rejects a missing clock, and retains the staged trace without
overwriting an existing destination.

Run the public consumer with:

```text
GOWORK=off go test ./... -count=1
```

The repository’s accumulated session regression set, focused normal/race
tests, pinned lint/staticcheck, vet, formatting, and coverage registration were
green from the earlier implementation worktree. The historical shipped
credential-free replay probe explicitly made no physical-device or acoustic
claim. The exact post-C79 validation is recorded below; script CI, independent
review, guarded merge, and the post-merge vertical probe remain external
handoff gates.

The historical same-task continuation at `8b2983338ea449fd739994e8fbbc958581bd2e7b`
repeated the owned checks: sessiontrace normal `24` tests across 3 packages,
sessiontrace race `20` tests across 3 packages, CLI trace normal `25`, CLI trace
race `24`, the GOWORK=off consumer, and accumulated normal regressions at
`COUNT=1` all passed. Fetched `origin/main` is
`ea53be13ce5e4ef14fd8c89c695c21744a1f7686` and is not yet an ancestor. The
shared gates still fail only on the C79-leased findings: Wire registration for
`go-agent-runtime/services/sessiontrace/wire/wire_gen.go` and the stale
`prepareTrace` architecture baseline/generated-file entries. At that
checkpoint C79 `work-task-25` was still `in-review` with PR #470 open, so those
files remained untouched. C79 has since been guarded-merged and released;
the current candidate applies only the demonstrated shared repairs.

## Post-C79 integration and current candidate

C79 PR #470 merged to `origin/main` at
`1a8467246c6607a06ffc7289075da2595724ce8b`. This isolated worktree merged
that exact main revision with the prior C112 candidate at
`8e0465c9f9c1bea49afb422713de5313d900407a`, preserving both parents. The
demonstrated shared repairs are committed at
`55565b8bc6cb31cf574ebca0761c698dee812351`. The
only post-release shared changes are the sessiontrace Wire registration in
`scripts/wire-packages.txt`, the corresponding generated-file registration in
`docs/architecture/architecture-policy.json`, and deletion of the stale
`prepareTrace` baseline fragment.

At the current source checkpoint, focused sessiontrace normal tests passed
`33` tests across 3 packages, race tests passed `55` tests across 3 packages,
CLI trace/audio normal passed `25`, CLI trace race passed `24`, and the
external `GOWORK=off` consumer passed. The accumulated regression matrix
passed at `COUNT=1` in normal, coverage, and race modes, including 20
high-rate tool/audio trials. `make build`, `make fmt`, `make vet`, pinned lint
(`golangci-lint 2.9.0`), pinned staticcheck (`2026.1`), coverage registration,
`make wire-check`, and `make architecture-size-check` all passed; the latter
checked 192 packages, 1911 files, and 28244 functions.

The rebuilt `/tmp/yui-c112-current.2AiBKY/yui` replayed both credential-free
fixtures with `--record-dir` and `--trace-audio`. The tool fixture preserved
`PROBE_TOOL_MARKER_9182` and `strict replay continuation`, produced the exact
4800-byte recorded PCM and a 4844-byte speaker-enqueued trace WAV. The
interruption fixture completed with a 3840-byte recording, a 3884-byte trace
WAV, and the exact 2400-byte healthy tail after the 1440-byte interrupted
prefix. Both manifests and contiguous transcripts matched their fixtures, and
no credential-like strings were found. Native Windows hardware and physical
acoustic testing remain explicitly out of scope.
