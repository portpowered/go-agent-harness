# C91 admission and baseline inventory

This checkpoint was recorded before changing any C91-owned source.

- Project/contract: `audio-runtime` / `audio-runtime-v1`.
- Canonical admission: `project-control.py verify-work --type task --name audio-runtime-c91-retire-cli-capture-claim-runtime --root "$FACTORY_ROOT"` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c91-retire-cli-capture-claim-runtime"}`.
- Canonical manifest authority hashes read from the admitted `$FACTORY_ROOT` manifest: source plan `f715163fb20f46a18837d4a4d19ff6d880aaadf8dbf40acfff88a0a6c5800d37`; request `4d53be6795ea189d5ae3aac727a76ea5dc5ac3f07c3a5e6280a2bc1e9ddcfeb0`; acceptance `edd1f5cbca1de8b49f96cdf8f039732d28a0082ef3718015e10535ff2e6a441a`.
- Isolated branch: `codex/audio-runtime-c91-retire-cli-capture-claim-runtime`, matching `prd.json.branchName`.
- Candidate base/current `HEAD`: `59af6325614d80173447fe2018a0471e27b4e7b1`.
- Fetched `origin/main`: `59af6325614d80173447fe2018a0471e27b4e7b1`; it is an ancestor of the candidate.
- Required startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21` is an ancestor of the candidate.
- Accepted-main legacy source: `agent-cli/internal/services/internal/agentruntime/session_capture_claim.go`, exactly 317 physical lines at `59af6325614d80173447fe2018a0471e27b4e7b1`; local baseline SHA-256 `1747b40578f4788b1b698495652f7e8f85abbff278eab904581cd38edcc8b6cc`.
- The isolated worktree was clean before this evidence file was added. No running host checkout was merged, reset, or edited.

## Production symbol and caller inventory

The accepted baseline search covered `ensureSessionRecordingClaim`,
`acquireSessionRecordingClaim`, `SessionRecordingClaimError`,
`SessionRecordingClaimHolder`, `ErrSessionRecordingDestination*`, and the
claim `publish`/`release` methods. The production callers are:

- `session.go:24`
- `session_audio_in.go:232,288`
- `session_audio_out.go:56,125`
- `session_duration.go:72,123`
- `session_image.go:125,189`
- `session_instructions.go:43,88`
- `session_recording.go:122,174,252,355`
- `session_runtime_plan.go:327,490-517`
- `session_text.go:40`
- `session_recording_directory_claim.go:39,127,145,155` consumes the shared holder/error aliases and path field.

The direct C91 test is
`agent-cli/internal/services/internal/agentruntime/session_capture_claim_test.go`.
All listed caller files and the directory-claim implementation are outside
the C91 mutation lease and remain byte-identical through the disjoint service
checkpoint. C79 retains the shared `scripts/wire-packages.txt` and
`docs/architecture/architecture-size-baseline.json` leases; neither is edited
here.

## Canonical task/review state

The `~default` work list was read with all terminal history. C91 is
`work-task-160`, state `init`, with no concluded C91 review row, rejection, or
repair finding. The handoff's preceding C89 failure and C79 shared-file lease
are preserved dependency context, not C91 review findings. Local `progress.txt`
was read and is a historical ledger whose current head predates C91; it is
preserved unchanged.
