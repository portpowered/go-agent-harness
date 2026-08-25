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
