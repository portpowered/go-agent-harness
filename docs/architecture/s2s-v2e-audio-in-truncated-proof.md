# s2s vertical v2e — truncated audio input terminates cleanly

Status: **proven in-repo** (2026-08-24) by a hermetic, CLI-driven probe lane
with no network and no credentials.

## What is proven

Truncated source audio (a file cut mid-word, so VAD may never observe
end-of-speech) cannot wedge a speech-to-speech session: the session ends
within the probe deadguard (`probeScenarioDeadline`, 30s) with an observable
disposition of the buffered partial audio.

The proof drives the real `agent probe run` CLI command over the T1
record/replay transport (no internal Go API is called directly from a test):

- `TestProbeRunV2EAudioInTruncated16kCommitsPartialUtterance`
  (`agent-cli/internal/cli/probe_test.go`): scenario
  `s2s-v2e-audio-in-truncated-16k` replays fixture
  `s2s_v2e_audio_in_truncated_16k.session.json`, where end-of-input arrives
  mid-word and the provider acknowledges `input_audio_buffer.committed`. The
  run must exit zero and satisfy both a `transcript-contains` expectation and
  a `buffer-disposition: committed` expectation.
- `TestProbeRunV2EAudioInTruncated24kDiscardsPartialUtterance`: scenario
  `s2s-v2e-audio-in-truncated-24k` replays
  `s2s_v2e_audio_in_truncated_24k.session.json`, where the buffered audio is
  explicitly discarded via `input_audio_buffer.discarded`
  (`buffer-disposition: discarded`) before a clean close.

## Buffer disposition observability

The replay executor extracts the disposition from server-to-client fixture
records (`replayBufferDisposition` in `agent-cli/internal/cli/probe.go`) into
the new `probe.ObservationSnapshot.BufferDisposition` field. The measurable
expectation `buffer-disposition` (`go-agent-loop/pkg/probe/expect.go`,
`ExpectBufferDisposition`) accepts only `committed` or `discarded`; a session
that ends with neither reports `uncommitted` and fails the expectation, so a
clean exit can never mask a silently wedged buffer.

## Negative control

`TestProbeRunV2ENegativeControlFailsOnUncommittedBuffer` replays
`s2s_v2e_audio_in_truncated_uncommitted.session.json`: the audio is appended,
but no commit or discard ever occurs even though the session closes cleanly.
The proof must FAIL (non-zero exit) with diagnostics naming
`expected "committed", actual "uncommitted"` — proving that suppressed buffer
commit cannot pass silently.

`TestV2EScenariosReferenceCommittedTruncatedCorpus` additionally asserts the
scenarios reuse the committed corpus fixtures
`go-agent-loop/testdata/audio/truncated_16k.wav` and
`go-agent-loop/testdata/audio/truncated_24k.wav`; no new audio fixtures were
required for this vertical. The synthetic replay fixtures added under
`agent-cli/internal/cli/testdata/probe-fixtures/` are additive-only testdata.

---

## Recut appendix (2026-08-25)

This document was carried byte-identical (blob-hash verified) from orphaned
PR #167 head `79a54e5790a417bfaaf0807a05f668426b94c9ad` onto the fresh branch
`s2s-v2e-audio-in-truncated-recut`, a descendant of `origin/main` =
`c5f576918e23be2e9c05dade826471c724f60eea`. The framework additions were
reapplied as a semantic union beside main's tool-result expectation family
(#170: `ExpectToolResultDelivered` / `ExpectToolResultDiscarded` /
`ExpectNoOrphanedToolResult`) — no arm of either family was removed from the
kind constants, `Evaluate`, typed validation, deadguard allowlist, or the CLI
alias map.

Direct CLI verification on the recut head, run 2026-08-25. Working directory
`agent-cli/internal/cli`; invocation shape confirmed against
`agent-cli/internal/cli/probe.go` (`probe run <scenario> --replay <fixture>

| scenario | command | exit | surfaced outcome |
|---|---|---|---|
| 16k | `go run ../../cmd/agent probe run testdata/probe-scenarios/s2s-v2e-audio-in-truncated-16k.scenario.json --replay testdata/probe-fixtures/s2s_v2e_audio_in_truncated_16k.session.json --json` | **0** | `"pass":true`; expectations `transcript-contains passed:true`, `buffer-disposition passed:true`; summary `{"total":1,"passed":1,"failed":0,"status":"pass"}` |
| 24k | `go run ../../cmd/agent probe run testdata/probe-scenarios/s2s-v2e-audio-in-truncated-24k.scenario.json --replay testdata/probe-fixtures/s2s_v2e_audio_in_truncated_24k.session.json --json` | **0** | `"pass":true`; expectations `transcript-contains passed:true`, `buffer-disposition passed:true`; summary status `pass` |
| uncommitted-negative | `go run ../../cmd/agent probe run testdata/probe-scenarios/s2s-v2e-audio-in-truncated-uncommitted-negative.scenario.json --replay testdata/probe-fixtures/s2s_v2e_audio_in_truncated_uncommitted.session.json --json` | **1** | `"pass":false` with `"error":"probe expectation \"buffer-disposition\" mismatch: expected \"committed\", actual \"uncommitted\""`; summary `{"total":1,"passed":0,"failed":1,"status":"fail"}`; CLI prints `Error: 1 of 1 probe scenarios failed` |

Union gate evidence (both families alive together):

```bash
go test ./go-agent-loop/pkg/probe/ -run 'TestEachMeasurableExpectationPassesAndFails|TestEvaluateBufferDisposition|TestMalformedExpectationsHaveTypedValidationIdentity' -count=1 -v   # ok — table covers tool-result rows AND buffer-disposition
go test ./agent-cli/internal/cli/ -count=1                                                                                                                                            # ok — both derivations wired
cd go-llm-gateway && go test ./internal/sessionfixturevalidator/ -count=1                                                                                                              # ok — exact-count recomputed green on this head
```
