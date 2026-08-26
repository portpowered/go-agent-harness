# S2S lane manifest — canonical coverage list

Generated 2026-08-25 from `design.md` Part 8.2/8.1/8.3, `docs/architecture/s2s-program-status-2026-08-17.md` §2/§6, and a live `gh pr list --state all --limit 300` sweep. This is the source of truth the ideafy meta-planner diffs against during its periodic taxonomy audit (see `factory/workstations/ideafy/AGENTS.md`).

Scope note: this manifest currently covers the vertical/milestone/acceptance/WebRTC tail of the program (43 lanes) — the category that was silently never submitted for most of one overnight run because the planner only tracked its own `docs/temp/checklist.md`, not the full canonical taxonomy. It does NOT yet enumerate the full ~152-lane program (the original `batches/s2s-program.json`/`s2s-localai.json` source files referenced in the status doc are not present on this host). If those files or an equivalent full inventory are ever located, extend this manifest to the full set rather than replacing it.

Board naming convention: every lane below is submitted with an `s2s-` prefix (e.g. `v6b-error-disconnect` -> board name `s2s-v6b-error-disconnect`).

**Naming drift warning:** `design.md` and the status doc do not always agree on a lane's exact suffix, and lanes actually submitted to the board have sometimes used a third variant. Example: `design.md` Part 8.2 names one vertical `s2s-v5a-image-and-text-in`; the status doc's inline inventory instead lists `s2s-v5a-default-toolset-active` as v5a and moves the image/text-in claim elsewhere; a board batch submitted 2026-08-25 used `s2s-v5a-image-and-text-in` again. Treat the `v<N><letter>` token (e.g. `v6b`, `v9`) as the stable identity, not the full string — before concluding a lane was "never submitted," grep `gh pr list --state all` and prior batch files for that short token, not just the exact manifest string, and compare the "Proves" claim, not just the name.

## Verticals (28) — design.md Part 8.2 / status-doc §2

- `s2s-v1-text-in-audio-out` — text -> streamed audio deltas -> playable WAV; transcript non-empty; first-audio tick recorded _(history: #159:MERGED)_
- `s2s-v2a-audio-in-basic` — short corpus utterance in -> audio out with RMS above the silence threshold _(history: #161:MERGED)_
- `s2s-v2b-audio-in-long` — explicit append/commit/clear event sequences over a long utterance, not just final audio _(history: NEVER SUBMITTED)_
- `s2s-v2c-audio-in-silence` — silence_* and noise_* inputs produce zero commits and zero turns _(history: #168:CLOSED, #176:MERGED)_
- `s2s-v2d-audio-in-multi-utterance` — a long utterance streams as multiple appends producing exactly one commit _(history: #165:CLOSED)_
- `s2s-v2e-audio-in-truncated` — truncated_* clips terminate cleanly, no wedged buffer _(history: #167:CLOSED, #179:MERGED)_
- `s2s-v3a-barge-in-basic` — overlap_* cancels the in-flight response at the provider; latency measured in ticks _(history: NEVER SUBMITTED)_
- `s2s-v3b-barge-in-during-tool` — interruption mid-tool-call does not orphan the tool result _(history: #164:CLOSED)_
- `s2s-v3c-barge-in-repeated` — repeated interruption; message counts reconcile, no loss or duplication _(history: #195:OPEN)_
- `s2s-v4a-tool-single-call` — call -> executor -> result reaches the provider -> speech resumes _(history: #163:MERGED)_
- `s2s-v4b-tool-parallel-calls` — concurrent tool calls all succeed, results correctly paired back to their calls _(history: NEVER SUBMITTED)_
- `s2s-v4c-tool-error` — a tool call failure surfaces as a typed, observable error _(history: #162:MERGED)_
- `s2s-v4d-tool-timeout` — a tool call that never returns is bounded by an explicit timeout; session degrades gracefully _(history: #197:OPEN)_
- `s2s-v4e-tool-unknown` — a request for an unrecognized tool name yields a typed refusal, not a panic or hang _(history: NEVER SUBMITTED)_
- `s2s-v4f-tool-during-audio` — a tool call issued while audio is still streaming in does not corrupt or drop the in-flight audio turn _(history: NEVER SUBMITTED)_
- `s2s-v5a-default-toolset-active` — the default toolset is available and callable without extra configuration _(history: NEVER SUBMITTED)_
- `s2s-v5b-toolset-subset` — read() types image, text and audio results correctly across a restricted toolset _(history: #171:MERGED)_
- `s2s-v5c-toolset-none` — typed refusal via the existing output/refusal.go contract when no tools are configured; session stays alive _(history: #172:MERGED)_
- `s2s-v6a-error-auth` — an auth failure from the provider surfaces as a typed, observable error, session recoverable _(history: #160:MERGED)_
- `s2s-v6b-error-disconnect` — a midstream disconnect reports the correct terminal reason, provenance, and output-state _(history: NEVER SUBMITTED)_
- `s2s-v6c-error-rate-limit` — a provider rate-limit response surfaces as a typed, observable error _(history: NEVER SUBMITTED)_
- `s2s-v6d-error-malformed-response` — a malformed provider response is detected and surfaced as a typed error, not a hang or panic _(history: #166:CLOSED)_
- `s2s-v7a-metrics-modality` — per-modality metrics (audio/text/tool token counts) are emitted and reconcile against the observed delta stream _(history: PR #187 MERGED 2026-08-26)_
- `s2s-v7b-buffer-logs` — input and output buffer overflow/drop events are counted and observably logged, never silent _(history: NEVER SUBMITTED)_
- `s2s-v7c-metrics-reconcile` — the token counter and per-modality metrics reconcile EXACTLY with the observed delta stream _(history: NEVER SUBMITTED)_
- `s2s-v8-duplex-overlap` — two harnesses, different instructions, each side's PCM output driving the other's PCM input, one shared Deterministic clock, both sides recorded, parity-compared, clean leak-free termination _(history: NEVER SUBMITTED)_
- `s2s-v9-webrtc-device-roundtrip` — real microphone -> session -> real speaker on a host with audio devices; asserts emitted energy and a recognized transcript; SKIP with a recorded reason if no device exists _(history: NEVER SUBMITTED)_
- `s2s-v10-webrtc-external-source` — a go2rtc-fronted camera source drives a live session end to end; agent media probe reports its tracks; audio-only sources correctly report look() unavailable _(history: NEVER SUBMITTED)_

## Depth-5+ program milestones (5)

- `s2s-e2e-audio-roundtrip-proof` — prove audio in + audio out through the public CLI end to end _(history: #157:MERGED)_
- `s2s-e2e-multiturn-conversation` — a 3-7 turn audio conversation that holds together _(history: #158:MERGED)_
- `s2s-e2e-tool-call-conversation` — customer asks by voice, agent calls a specific CLI tool, response reflects the real result _(history: NEVER SUBMITTED)_
- `s2s-e2e-vision-describe` — customer asks by voice about a committed image, agent describes it from actual image content _(history: NEVER SUBMITTED)_
- `s2s-e2e-conversation-observability` — logs and recordings alone (not test instrumentation) prove the conversation happened _(history: NEVER SUBMITTED)_

## Acceptance gate (7)

- `s2s-e2e-customer-acceptance-probe` — the full acceptance-probe entrypoint gating the program _(history: NEVER SUBMITTED)_
- `s2s-acc-probe-goal-catalog` — a catalog of customer goals the acceptance probe fleet drives against _(history: NEVER SUBMITTED)_
- `s2s-acc-probe-stuck-detection` — the probe fleet detects and reports a session that got stuck, not just one that errored _(history: NEVER SUBMITTED)_
- `s2s-acc-probe-friction-report` — a friction report synthesized from probe fleet runs _(history: NEVER SUBMITTED)_
- `s2s-acc-fleet-gate` — the fleet-wide pass/fail gate combining all acceptance probes _(history: PR #190 MERGED 2026-08-26; main 58727d6)_
- `s2s-b4-fleet-composer` — fleet manifest composing every committed scenario x transport x repeats x concurrency, no silent caps _(history: NEVER SUBMITTED)_
- `s2s-b4-fault-injection` — transport-seam faults (mid-stream close, delayed/dropped frames, slow consumer, ICE failure) demonstrated to change observed session behavior _(history: NEVER SUBMITTED)_

## Remaining WebRTC engineering lanes (3)

- `s2s-b4-rtc-device-binding` — --audio-in-device/--audio-out-device feed/play WebRTC tracks through the same device registry as the WS path _(history: NEVER SUBMITTED)_
- `s2s-b4-rtc-cli-surface` — --transport webrtc, --signaling, --media-source wired through the Router with documented mutual exclusions _(history: NEVER SUBMITTED)_
- `s2s-b4-rtc-transport-parity` — one scenario run over WS and over RTC produces the same Projection; a behavioral difference between transports is a failure _(history: NEVER SUBMITTED)_

## Never-submitted at manifest generation time

UPDATE 2026-08-26: every lane below except `s2s-v2b-audio-in-long` (OPEN PR #185) was verified still unsubmitted and admitted in batch `s2s-post-restart-taxonomy-20260826` (28 ideas + loopback). Check board/PR history before re-admitting any of them.

- `s2s-v2b-audio-in-long`
- `s2s-v3a-barge-in-basic`
- `s2s-v3c-barge-in-repeated`
- `s2s-v4b-tool-parallel-calls`
- `s2s-v4d-tool-timeout`
- `s2s-v4e-tool-unknown`
- `s2s-v4f-tool-during-audio`
- `s2s-v5a-default-toolset-active`
- `s2s-v6b-error-disconnect`
- `s2s-v6c-error-rate-limit`
- `s2s-v7a-metrics-modality`
- `s2s-v7b-buffer-logs`
- `s2s-v7c-metrics-reconcile`
- `s2s-v8-duplex-overlap`
- `s2s-v9-webrtc-device-roundtrip`
- `s2s-v10-webrtc-external-source`
- `s2s-e2e-tool-call-conversation`
- `s2s-e2e-vision-describe`
- `s2s-e2e-conversation-observability`
- `s2s-e2e-customer-acceptance-probe`
- `s2s-acc-probe-goal-catalog`
- `s2s-acc-probe-stuck-detection`
- `s2s-acc-probe-friction-report`
- `s2s-acc-fleet-gate`
- `s2s-b4-fleet-composer`
- `s2s-b4-fault-injection`
- `s2s-b4-rtc-device-binding`
- `s2s-b4-rtc-cli-surface`
- `s2s-b4-rtc-transport-parity`

## How to refresh this manifest

```sh
gh pr list --state all --limit 300 --json title,headRefName,state,mergedAt > /tmp/all_prs.json
# then re-run the generation script this file's history section was built from,
# or manually update the _(history: ...)_ annotation per lane.
```

A lane's `_(history: ...)_` annotation reflects PR history only, not board completion — cross-check `gh pr list --state merged` before treating a lane as truly done, per the existing `AGENTS.md` §"prove the work is not already done" rule.
