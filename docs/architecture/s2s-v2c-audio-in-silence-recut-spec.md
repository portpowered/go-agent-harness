# Recut spec — s2s-v2c-audio-in-silence (#168)

Date: 2026-08-25. Evidence: `s2s-vertical-pr-disposition-triage-evidence.md` §1–§5,
table row in §9 of `s2s-program-status-2026-08-17.md`.

## Disposition

| axis | verdict |
|------|---------|
| Outcome already on main? | **No.** Main has zero commits touching `session_audio_in.go` since the merge base, and the silence/noise integration proof `session_audioin_silence_test.go` is ABSENT_ON_MAIN (evidence §2 §7). The silence vertical exists only on the PR head. |
| Conflict class | **None — merges clean.** `git merge-tree --write-tree origin/main 266f220` exits 0 (tree `c3c9c9185ce8`); GitHub reports MERGEABLE/UNSTABLE. The DIVERGENT blobs are ordinary PR-side edits to files main did not touch. |
| Recommendation | **Keep and recut** on fresh branch `s2s-v2c-audio-in-silence-recut` (unique per `git ls-remote origin refs/heads/s2s-v2c-audio-in-silence-recut` → empty, 2026-08-25). Smallest lift of the four: no textual conflicts at all; the only red is gofmt drift in the PR's own new test file. |

The 2026-08-24 failure mode does not apply: nothing of v2c is on main.

## Red checks — trivial and self-owned

Both failing runs die in the repo's fmt gate:

```
gofmt drift detected in agent-cli:
./internal/services/session_audio_in_internal_test.go
Run 'make fmt-fix' to rewrite files before rerunning 'make ci'.
```

(job log 97701834238, evidence §5). UNSTABLE = mergeable + red checks; the checks are
red only because the branch's own test file is not gofmt-clean.

## Changed-file list to carry over (from live head `266f220`, blob hashes in evidence §2)

- `agent-cli/internal/services/session_audio_in.go` — PR-side delta (+110/−8): 24 kHz WAV
  sources decode once and resample to the harness rate (`openSessionWAVSource` returns
  `audio.AudioSource`; `wavio` import); harness-rate payloads still stream frame-by-frame.
- `agent-cli/internal/services/session_audio_in_internal_test.go` — additions incl.
  `TestNewSessionWAVSourceResamples24kHz`; **run `make fmt-fix` on the recut branch
  before committing** — this exact file failed the fmt gate on the old head.
- `agent-cli/test/integration/session_audioin_silence_test.go` — carry verbatim
  (`TestSessionAudioInSilenceFixturesProduceZeroCommitsAndTurns`,
  `TestSessionAudioInNoiseFixturesProduceZeroCommitsAndTurns`,
  `TestSessionAudioInUtteranceFixtureProducesRealCommit`).

No scanned-root fixture is added (the integration test lives outside
`agent-cli/test/integration/testdata`), so the validator exact-count assertion stays at
main's value (23 today) — verify by running it.

## Conflict notes

None. `git merge-tree --write-tree --name-only origin/main 266f220f6561` exits 0 with no
conflicted paths. No resolution decisions exist for this lane — that is precisely why it
is the cheapest recut.

Alternative considered and recorded per policy: landing #168 as-is with a maintainer
fmt fix would avoid a recut branch entirely, but this lane's discipline forbids pushing
to the orphaned branch (no force-push/mutation authorization, status doc §4.7), and the
PR has sat without an active lane since opening — a fresh branch under an active lane is
the auditable path.

## Recut plan

1. `git fetch origin && git switch -c s2s-v2c-audio-in-silence-recut origin/main`
2. Carry the three files from head `266f220f6561e5fa9603ae04b22bfef75cfa7ff6`
   (cherry-pick `266f220` or checkout paths).
3. `make fmt-fix` before the first commit; confirm `git status` is clean afterward.
4. Lease: the three paths named here. No production code beyond the named file's
   carried delta.

## Verification commands

```bash
# fmt gate must pass first (this is what was red)
make fmt

# unit surface incl. the resampler
go test ./agent-cli/internal/services/ -run 'TestNewSessionWAVSourceResamples24kHz|TestNewSessionWAVSourceStreamsFrameByFrame|TestSessionWAVSourceZeroPadsFinalShortFrame' -count=1 -v

# the v2c outcome itself: silence and noise produce zero commits and zero turns;
# a real utterance still produces its commit
go test ./agent-cli/test/integration/ -run 'TestSessionAudioInSilenceFixturesProduceZeroCommitsAndTurns|TestSessionAudioInNoiseFixturesProduceZeroCommitsAndTurns|TestSessionAudioInUtteranceFixtureProducesRealCommit' -count=1 -v

# validator gate unchanged
cd go-llm-gateway && go test ./internal/sessionfixturevalidator/ -count=1 && cd ..
```

Note: the original lane PRD `tasks/todo/s2s-v2c-*.md` is absent from every ref in this
repository; commands reconstructed from the PR's own test symbols at head `266f220`.
