# C109 current-head hermetic rejection — 2026-09-13

This evidence-only update is committed at C109 head `9e0ccd5e7a17a8385042e634f95cedd7cbcf2b46`.
The CI run below tested its parent `ab0ada542`; the update changes no
executable input or out-of-lease source and preserves that exact rejection.

The exact current-head script-CI result for PR #495 was read from completed
run `34754953782` at candidate `ab0ada54292b9682879a98fc3b470e3b8c30df6a`.
The CI merge checkout was `5222672fe985c95f9f32128905fe687959887d0b`; the
run JSON and hermetic job log hashes are retained in
`ci-rejection-34754953782.json`.

The review-time fetch is `origin/main=071b0abfd67501db61e3c1929971c6dd6e77eb62`,
already ancestral to the candidate. Accepted main remains
`d4766c3dbbf2c198142047ead4449d58dd47d485`; preserved C61 and C83 remain
`8e8177c031a7b3b9322d712af19970e13fa7a1bc` and
`22cc6769aaf06d1e2c1275b064cc7ec29de3e371`.

Eight required lanes passed: coverage, integration, macOS audio release, race,
static, unit, WebMCP Chrome and Windows audio portable. Hermetic failed only
these two unchanged tests:

- `agent-cli/internal/services/internal/agentruntime/s2s_room_realtime_replay_overlap_test.go:624`,
  `TestRunRoomWithResult_BidirectionalOverlapRecordsPeerOnlyEvidence`, which
  missed its explicit max-turn boundary with `context deadline exceeded`.
- `agent-cli/internal/services/internal/agentruntime/session_audio_out_test.go:79`,
  `TestRunSessionWithAudioOut_FinalizesPlayableWAV`, which observed 480 WAV
  samples instead of the exact ordered response.

The hermetic job log shows the `agent-cli/internal/services/internal/agentruntime`
package failure under `make test-hermetic`; neither path is changed by the C109
branch versus `origin/main`. These are inherited failures outside C109's
evidence-only lease. C109 does not edit, waive, relabel or claim either one
repaired; the primary must route the exact signatures to the existing
room/liveness and audio-output owners.

Fresh owned validation remains green after the rejection: `test_analyze.py`
passes 4/4; `verify.py --mode all` passes all eight checks and 19 negative
fixtures; the credential-free browser/audio/tool matrix passes 18/18 in
253.568 seconds under 90/300 seconds; and malformed/canceled passes 3/3 in
29.471 seconds under 60/180 seconds. Both public reports are bound to runner
SHA-256 `705bc3167678b83158fc925e132fe39d1fdd4315c845360a8686eee8db9b6efe`
and have clean process groups.

This checkpoint claims no CI green, review, merge, C61/C83 acceptance, vertical
probe, hardware/acoustic proof or project completion. It does not resubmit the
unchanged candidate. After the external failures are repaired and reach the
accepted mainline, the primary resumes this same C109 task for current-head
revalidation; C109's broad AUDIO, DEVICE, EMBED, SERVICE, TRACE, REPLAY,
FAILURES, QUALITY and PARITY gates remain open.
