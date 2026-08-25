# s2s program — real status and restart plan (2026-08-17)

> **v4a-tool-single-call status update (2026-08-24).** The vertical's
> CLI-verified hermetic integration proof landed in
> `agent-cli/test/integration/session_tool_single_call_test.go` (PR:
> s2s-v4a-tool-single-call). Proven end to end through the real `agent session`
> CLI over the record/replay transport: a file-backed spoken request whose
> replayed provider exchange carries exactly one named function tool call with
> expected arguments, output speech produced after the tool call, and a
> deterministic negative control proving the exactly-one invocation assertion
> rejects a suppressed tool call. Remaining for full v4a "executor round trip"
> proven status (out of this test lane's lease, filed separately): the session
> loop in `services/session_live.go` constructs its `agentloop` without
> `WithToolExecutor`, so the composed tool executor never executes the named
> tool, and the realtime outbound translation does not yet forward tool
> results to the provider. The positive-path test skips with that exact reason
> and turns green automatically once that wiring lands.

> **CLI-verifiable evidence contract.** The proving test is
> `agent-cli/test/integration/session_tool_single_call_test.go`. Its command
> shape is the real session surface (shown with the test's temporary replay
> and output paths):
> `agent session --audio-in go-agent-loop/testdata/audio/truncated_16k.wav --replay <t.TempDir>/tool-single-call.session.json --audio-out <t.TempDir>/response.wav --max-duration 3s`.
> The replay is a strict, offline OpenAI Realtime WebSocket capture assembled
> from the committed smoke capture and the committed audio corpus; it accepts
> the exact input frames, commit, and response request before delivering one
> `get_weather` call with `{"city":"Lisbon"}` and scripted output audio. The
> transport-level assertion is ordered and counted: one named provider call
> with the expected arguments, then valid non-empty output audio. The executor
> invocation and correlated result-delivery assertions are present in the
> positive test but remain skipped until the out-of-lease session wiring emits
> the provider-facing result; they must then sit between the call and resumed
> audio. The no-invocation control uses the identical CLI and audio input with
> the call suppressed; it still emits response audio, but the shared
> exactly-one assertion fails with a missing `get_weather` invocation.
> The longer `tool_request_16k.wav` corpus asset is 20.5 seconds and does not
> fit the session runtime's current three-second bounded replay window, so it
> is not substituted silently. This evidence is specifically the v4a tool
> boundary; it is not a claim about v1 text-in/audio-out, v2a basic audio-in,
> v4c tool-error handling, or v6a auth-error handling.

Ground truth from git + GitHub, not from the factory board (the board was
in-memory and is gone). Everything here is reproducible with the commands in §8.

---

## 0. The one-paragraph version

152 lanes were planned. **77 merged, 15 are stuck in review, 58 never started —
and not one of the 58 can start**, because every one is transitively blocked
behind the 15. The 77 that merged are foundation: file splits, coverage, new
packages, CI gates. The 15 that are stuck are precisely the *integration seams*
— the code that connects those new packages to the running CLI — and they are
stuck for one shared, mechanical reason (§3). Net effect: on `main` today the
product does **text in → audio out**, and nothing else. Audio in, images, tools,
microphones, WebRTC, and the entire probe/acceptance layer are all unreachable.
**No acceptance criterion for this program is currently testable.**

---

## 1. What actually works on `main` today (verified)

- `main` CI: **green** (last 4 runs success).
- Builds clean with `CGO_ENABLED=0 go build ./agent-cli/... ./go-llm-gateway/...`.
  A plain `go build` fails on this host — no working cgo toolchain. That is a
  local gap, not a repo defect, and is exactly what PR #77 addresses.
- Registered CLI surface (`agent-cli/internal/cli/routes.go`):
  `agent ask`, `agent chat`, `agent tool`, `agent interaction replay`,
  `agent session` (+ `show`/`list`/`delete`), `agent config add-local`.
- `agent session` flags that exist: `--prompt`, `--system-prompt`, `--model`,
  `--provider`, `--api-key`, `--base-url`, `--max-duration`, `--record`,
  `--replay`, `--audio-out`.
- `agent session` flags/commands that **do not exist**: `--audio-in`,
  `--image`, `--tools`, `--audio-in-device`, `--audio-out-device`,
  `--record-dir`, `--transport webrtc`, `agent devices list`, `agent probe run`.
- `agent-cli/internal/services/` contains `session_audio_out.go` and no
  `session_audio_in.go`, `session_image.go`, or `session_tools.go`. Those files
  exist **only inside the 15 open PRs**.

---

## 2. Inventory

| bucket | count |
|---|---|
| Lanes merged | **77** |
| Repair lanes merged (out-of-band fixes) | 4 (#66, #128, #136, #137) |
| Lanes with an **open PR** | **15** |
| Repair lane with an open PR | 1 (#138) |
| Lanes with **no branch and no PR** (never started) | **58** |
| Operator loopback items (no PR by design) | 2 |
| **Total named lanes** | **152** |

There are no orphan branches: every lane either has a PR or has nothing.
Nothing is half-pushed.

### 2.1 What merged (77) — by group

| group | count | what it delivered |
|---|---|---|
| `s2s-b1-split-*` | 6 | broke up the six largest files (1164, 1141, 1135, 714, 616, 379 lines) |
| `s2s-b1-cov-*` | 16 | test coverage across cli, tools, messages, logger, providers, models |
| `s2s-b1-*` gates | 4 | coverage manifest gate, wall-clock budget gate, size/complexity lint, wire composition boundary |
| `s2s-b2-*` packages | ~25 | `pkg/wavio`, `pkg/transport`, `pkg/platform/clock`, `pkg/metrics`, `pkg/transcript`, `pkg/audiofixture`, device registry + virtual loopback + WASAPI + ALSA, resampler, audio corpus, wire ports, mock injection |
| `s2s-b3-*` | ~14 | `--audio-out`, `--prompt`, AGENTS.md on the session path, turn management, AGENTS.md profiles, parity projection/normalize/compare, probe scenario model + expectations + guard + tick sequencing, functional layout |
| `s2s-b4-rtc-*` | 4 | WebRTC transport contract, signaling, peer-connection lifecycle, external media source |
| `s2s-lai-*` | 3 | LocalAI realtime fixture, LocalAI gateway provider, qwen3-tts GGUF format check |
| `s2s-b0-*` | 1 | `gpt-realtime-2.1-mini` accepted; realtime model set is data, not a constant |

### 2.2 The 15 open PRs — what each one is, and what to do with it

Ranked by **how many of the 58 not-started lanes it transitively gates.**

| PR | lane tag | operational task | actual diff | comments | gates | verdict |
|---|---|---|---|---|---|---|
| **#118** | `s2s-b2-transport-grok-retype` | Retype the grok provider onto `pkg/transport` and migrate its consumers | **1 file, +7/−3** (`grok/dialer.go`) | **41** | **42 of 58** | **RESTART** |
| **#98** | `s2s-b3-session-audio-in-file` | `agent session --audio-in` — feed a WAV into a realtime session | +1465/−0, 3 files (+10 to `cli/session.go`) | 59 | **24 of 58** | CONTINUE |
| **#83** | `s2s-b3-session-tool-executor-wiring` | Wire the tool executor into the realtime session path | +241/−0, 2 **new** files, touches nothing existing | 55 | 10 of 58 | **RESTART** |
| **#97** | `s2s-b3-session-image-input` | `agent session --image` — send an image into a turn | +948/−0 (+12 to `cli/session.go`) | 56 | 7 of 58 | CONTINUE (blocked, §4.2) |
| **#113** | `s2s-b2-audio-device-source-and-sink` | Device-backed AudioSource/sink behind the existing interface | +758/−0, 4 **new** files in `internal/audio` | 62 | 6 of 58 | CONTINUE (blocked, §4.2) |
| **#132** | `s2s-b4-rtc-audio-track-egress` | Outbound WebRTC track, PCM16 → Opus | +1167/−0, `pkg/transport/rtc` | 28 | 6 of 58 | CONTINUE — closest to done |
| **#133** | `s2s-b4-rtc-audio-track-ingress` | Inbound WebRTC track, Opus → PCM16 + jitter | +1381/−0, `pkg/transport/rtc` | 17 | 6 of 58 | CONTINUE — closest to done |
| **#110** | `s2s-b3-session-recording-flags` | `--record-dir` and the both-side recording surface | +1424/−7 (only real edit: `cli/session.go`) | 62 | 1 of 58 | CONTINUE — needs #98's seam |
| **#107** | `s2s-lai-local-tier-conformance` | Prove which milestones LocalAI can gate vs OpenAI | +1778/−0, new `test/localai` module | 21 | 1 of 58 | CONTINUE |
| **#116** | `s2s-b3-devices-list-command` | `agent devices list` (table + JSON) | +400/−0 — adds `cli/devices.go`, **never touches `cli/routes.go`** | 36 | 0 | **CONTINUE — one-line fix** |
| **#77** | `s2s-b1-hermetic-test-target` | No-CGO, no-microphone CI test tier | +68/−0 (`ci.yml`, `Makefile`, doc) | 34 | 0 | **MERGE FIRST — smallest** |
| **#46** | `s2s-b1-cov-session-storage` | Cover `session/storage.go` | +635/−0, one test file | 54 | 0 | **SPLIT — it found a real bug** |
| **#114** | `s2s-b1-cov-services-chat-core` | Cover the chat split's four components | +941/−0, 4 test files + golden | 55 | 0 | DESCOPE or gate (§4.2) |
| **#68** | `s2s-lai-realtime-client-lib-decision` | **Decide** whether to adopt `WqyJh/go-openai-realtime` | ADR + 522 lines of eval client | **67** | 0 | **OPERATOR DECISION — close the lane** |
| **#102** | `s2s-b2-device-macos-coreaudio` | CoreAudio capture/render backend | +494/−0, `device_darwin.go` | 57 | 0 | **DROP on this host** |

Also open: **#138** `s2s-repair-rtc-opus-codec-owner`, an out-of-band repair lane.

### 2.3 The 58 that never started

None has a branch. All are blocked. Grouped by layer, with the immediate blocker.

**Ready the moment a specific PR merges (10 lanes) — this is the frontier:**

| lane tag | operational task | waiting on |
|---|---|---|
| `s2s-b2-transport-recordreplay-retype` | Retype record/replay dialers onto `pkg/transport` | #118 |
| `s2s-e2e-audio-roundtrip-proof` | MILESTONE: audio in + audio out end-to-end through the CLI | #98 |
| `s2s-b3-session-vad-gating` | Wire the energy VAD into the session input path | #98 |
| `s2s-b3-session-audio-in-device` | `--audio-in-device` — capture from a real mic | #98, #113 |
| `s2s-b3-session-audio-out-device` | `--audio-out-device` — play to a real speaker | #113 |
| `s2s-b3-session-default-toolset` | Default tool set for realtime sessions, `--tools` | #83 |
| `s2s-b3-session-tool-definitions` | Tool schemas + argument validation | #83 |
| `s2s-b3-session-replay-and-parity-commands` | `agent session replay` / `agent session parity` | #110 |
| `s2s-b4-rtc-recording` | Record WebRTC both sides with verbatim RTP | #132, #133 |
| `s2s-lai-blind-probe-local-tier` | Blind probe fleet against LocalAI, OpenAI for final gate | #107 |

**The probe chain (6 lanes) — the choke point for everything else:**

`s2s-b2-transport-recordreplay-retype` → `s2s-b3-probe-replay-transport`
(run probe scenarios against the replay transport, the CI-gating path) →
`s2s-b3-probe-runner-jsonl` (probe runner, JSONL results, summary artifact) →
`s2s-b3-probe-cli-surface` (`agent probe run` — **the command every vertical is
delivered through**). Plus `s2s-b3-probe-live-transport` (opt-in live API) and
`s2s-b2-record-fixtures` (`make record-fixtures`).

`s2s-b3-probe-cli-surface` alone gates **36 lanes**. Its whole chain hangs off
**#118**.

**The verticals (28 lanes) — all gated on `s2s-b3-probe-cli-surface`:**

`v1-text-in-audio-out` · `v2a-audio-in-basic` · `v2b-audio-in-long` ·
`v2c-audio-in-silence` · `v2d-audio-in-multi-utterance` · `v2e-audio-in-truncated` ·
`v3a-barge-in-basic` · `v3b-barge-in-during-tool` · `v3c-barge-in-repeated` ·
`v4a-tool-single-call` · `v4b-tool-parallel-calls` · `v4c-tool-error` ·
`v4d-tool-timeout` · `v4e-tool-unknown` · `v4f-tool-during-audio` ·
`v5a-default-toolset-active` · `v5b-toolset-subset` · `v5c-toolset-none` ·
`v6a-error-auth` · `v6b-error-disconnect` · `v6c-error-rate-limit` ·
`v6d-error-malformed-response` · `v7a-metrics-modality` · `v7b-buffer-logs` ·
`v7c-metrics-reconcile` · `v8-duplex-overlap` · `v9-webrtc-device-roundtrip` ·
`v10-webrtc-external-source`

**The milestones (5 lanes) — the things you actually asked for:**

| lane tag | operational task | chain |
|---|---|---|
| `s2s-e2e-audio-roundtrip-proof` | Prove audio in + out through the public CLI | #98 |
| `s2s-e2e-multiturn-conversation` | A 3–7 turn audio conversation that holds together | ← roundtrip-proof |
| `s2s-e2e-tool-call-conversation` | Customer asks by voice, agent calls a specific CLI | ← multiturn + default-toolset (#83) |
| `s2s-e2e-vision-describe` | Customer asks by voice about an image, agent describes it | ← multiturn + #97 |
| `s2s-e2e-conversation-observability` | Logs + recordings actually prove the conversation happened | ← tool-call + vision |

**The acceptance gate (7 lanes) — the "all probes said it was easy" bar:**

`s2s-e2e-customer-acceptance-probe` → `s2s-acc-probe-goal-catalog` +
`s2s-acc-probe-stuck-detection` → `s2s-acc-probe-friction-report` →
`s2s-acc-fleet-gate`. Plus `s2s-b4-fleet-composer`, `s2s-b4-fault-injection`,
`s2s-b4-fleet-summary-artifact`.

**Remaining WebRTC (3 lanes):** `s2s-b4-rtc-device-binding`,
`s2s-b4-rtc-cli-surface` (`--transport webrtc`), `s2s-b4-rtc-transport-parity`.

---

## 3. What failed, and why — the single shared defect

Look at the `actual diff` column in §2.2. **Fourteen of the fifteen stuck PRs
have zero deletions.** They add new, self-contained, heavily-tested files and
modify almost nothing that already exists.

That is not a coincidence — it is what the lane leases forced:

- **#116** adds `cli/devices.go` (181 lines) with tests, but `agent devices list`
  is registered in `cli/routes.go`, which the lane may not touch. So the command
  is unreachable and the reviewer correctly reports `unknown command`. The lane
  cannot fix this. **One registration line stands between this PR and working.**
- **#83** is titled *"Wire the tool executor into the realtime session path"* and
  adds `services/session_tools.go` plus a test. It wires nothing into anything.
  The session path is outside its lease.
- **#118** is titled *"Retype the grok provider onto pkg/transport"* and consists
  of **7 added and 3 removed lines in one file** after 41 comments and nineteen
  review passes. The consumers it must migrate are outside its lease.

So the program produced roughly **9,000 lines of well-tested code that nothing
calls.** The reviewers were right to block; the lanes were right that they could
not comply. The defect is in the batch design, not in either agent.

Two contributing defects, both now fixed at source:

1. **The `<CONTINUE>` deadlock.** `review` is a two-token join consuming
   `task:in-review` **and** `review:init`, and `process` mints that pair *only*
   on `<COMPLETE>`. A lane with out-of-lease acceptance criteria could never say
   `<COMPLETE>`, so review became structurally unreachable — its PR could never
   merge however green it was. One lane sat this way for ~17 hours on a CLEAN,
   mergeable PR; it merged 9 minutes after being told to stop. 16 of 19 open
   lanes were in the same state. Fixed by `AGENTS.md` rules 17.4/17.4.1/17.5
   (commits `e17e1f6`, `a5a41f3`).
2. **The ≤400-line lane bound counted test lines.** It blocked 9 of 15 coverage
   lanes whose diffs were nearly all tests. Amended to production lines only
   (`19f0d0f`).

Other fixes landed: `a868f31` / `685075e` (ideafy's self-heal commands omitted
`--server` and silently hit port 7437, so every queue reconciliation returned
`FACTORY_UNREACHABLE` while appearing to run), `4161ca2` / `34f8b2e` (rules doc).

**Hard stop:** the codex weekly quota hit 100% (`plan_type: pro`,
`balance: "0"`, `window 10080 min`, `resets_at 1787196674` = **2026-08-20
03:31:14Z**). Nothing runs until that resets or credits are added. This is
separate from the blockage above — the program was already stalled on §3 before
the quota ran out.

---

## 4. What to restart, what to continue, what to drop

### 4.1 Restart — the current PR is not salvageable

- **`s2s-b2-transport-grok-retype` (#118).** 7 lines after 19 review passes, and
  it gates 42 of 58 remaining lanes. Close it. Re-cut as one lane whose
  `changedPathLease` covers **both** `go-llm-gateway/pkg/providers/grok/**` and
  every consumer of the old dialer type. State in the payload that migrating
  callers is in scope.
- **`s2s-b3-session-tool-executor-wiring` (#83).** Re-cut with a lease that
  includes `services/session_runtime.go` and the realtime session path, so the
  word "wire" is actually executable.

### 4.2 Continue, but only after an operator decision

These are blocked on production contracts their lease forbids. Pick one policy
and apply it to all of them:

1. **File the missing production contracts as their own lanes**, gate these
   behind them with `DEPENDS_ON`. Faithful to the testing bar; adds lanes.
   *(Recommended.)*
2. **Widen the leases** so each lane writes its own integration point. Faster;
   breaks surface ownership; invites cross-lane conflict.
3. **Descope the criteria.** Cheapest; lowers the bar you set.

Affected: **#97** (production realtime session lacks the complete-message
contract), **#113** (needs real WASAPI frame I/O in `device_windows.go` plus
shared conformance infra), **#110** (needs a production seam owned by #98, a
different lane, with no `DEPENDS_ON` edge between them), **#114** (typed
command-error assertions, at-file path-escape, bounded scrollback — all
production APIs).

### 4.3 Continue as-is

- **#77** — 68 lines, CLEAN. Rebase and merge; smallest thing here, and it fixes
  the no-cgo build gap this host actually has.
- **#116** — widen the lease by one file (`cli/routes.go`) and it is done.
- **#132 / #133** — self-contained in `pkg/transport/rtc`, low comment counts, no
  cross-lane dependency. Closest to merging of anything non-trivial.
- **#98** — the second-most-valuable PR (gates 24). Verify the `+10` to
  `cli/session.go` genuinely reaches the session path, then rebase and land.
- **#107** — new isolated `test/localai` module; low risk.

### 4.4 Decide, don't build

- **#68 `s2s-lai-realtime-client-lib-decision`.** The lane's job was to *decide*
  whether to adopt `WqyJh/go-openai-realtime`. It has 67 comments, 12 commits,
  and has built 522 lines of evaluation client. **Read the ADR, make the call,
  close the lane.** Every further review pass is wasted quota.

### 4.5 Split

- **#46 `s2s-b1-cov-session-storage`.** The coverage lane did its job and found a
  **real production bug**: concurrent writes with the same ID corrupt the JSON
  file, `make test` fails, CI is red. That bug belongs in its own production
  lane. Keep the test file; let it go red until the fix lands, or land it skipped
  with a pointer to the bug lane.

### 4.6 Drop on this host

- **#102 `s2s-b2-device-macos-coreaudio`.** Its acceptance requires
  `TestCoreAudioDeviceRegistryHardware` on native macOS with real CoreAudio
  hardware and mic permission. This is a Windows workstation. No amount of
  factory throughput satisfies it. Descope to non-hardware assertions, or move
  the lane to a Mac.

### 4.7 Stale branches

Depth behind `origin/main` at shutdown. The top three drifted a further ~60–90
commits during the session; they are not rebasing on their own. **Rebasing them
requires a force-push, which was never authorised.**

```
s2s-b1-cov-session-storage            323
s2s-b1-hermetic-test-target           292
s2s-b3-session-tool-executor-wiring   270
s2s-b2-device-macos-coreaudio          61
s2s-b3-session-recording-flags         61
s2s-lai-realtime-client-lib-decision   48
s2s-lai-local-tier-conformance         48
s2s-b3-session-image-input             11
```

---

## 5. Ordered restart plan

Steps 1–3 are cheap and unblock disproportionately.

1. **Land #77** (68 lines, CLEAN). Rebase, merge. Fixes the no-cgo tier.
2. **Land #116** by adding `cli/routes.go` to its lease. One registration line
   makes `agent devices list` real.
3. **Answer §4.2** — pick policy 1, 2, or 3. Four PRs are frozen until you do.
4. **Close #118 and re-cut it** with a consumer-inclusive lease. This is the
   single highest-value action in the program: **42 of 58 remaining lanes** sit
   behind it, via
   `grok-retype → recordreplay-retype → probe-replay-transport →
   probe-runner-jsonl → probe-cli-surface → 28 verticals + acceptance gate`.
5. **Land #98.** Unblocks `e2e-audio-roundtrip-proof` — the first milestone that
   proves anything to a customer — plus 23 more.
6. **Close #68** with a decision.
7. **Re-cut #83**, then land #132 / #133 / #107, then the §2.3 frontier.
8. Only then does the vertical layer become submittable.

### 5.1 Fix the batch design before resubmitting

Otherwise the next 58 lanes reproduce §3 exactly:

- **Every lane that says "wire", "register", "enable", or "expose" must hold a
  lease covering the file where registration actually happens.** For CLI commands
  that is `agent-cli/internal/cli/routes.go`; for session features,
  `services/session_runtime.go`.
- **Add a review check: a lane claiming integration whose diff has zero deletions
  and touches no pre-existing production file has not integrated anything.** That
  one heuristic would have caught #83, #113, #116 and #118 on their first pass
  instead of their fortieth.
- **No acceptance criterion may name a contract outside the lane's lease.** If it
  must, add the `DEPENDS_ON` edge — and all edges must be in the same batch,
  since a `targetWorkName` that is only live on the board fails submission with
  `400 BAD_REQUEST`.
- **Prune the resubmit batch** to the 15 in-flight + 58 not-started lanes.
  Re-running merged lanes is wasted quota.

---

## 6. Factory operating knowledge — do not rediscover this

- **Never hand-move a token to `task:in-review`.** The paired `review:init` token
  will not exist, the join can never fire, and the only transition that can
  consume it is `review-loop-breaker`, which fails it. **`init` is the only safe
  manual destination.** Verified by experiment.
- **Stranded ideas are invisible to the FAILED count.** An idea at
  `to-complete/PROCESSING` with *no task token at all* is permanently dead —
  `consume` joins `idea:to-complete` with `task:to-complete` by SAME_NAME and
  there is nothing to join. Remedy: move the **idea** id to `init`. Hit 3 lanes;
  one (`s2s-b2-wire-mock-injection`) merged as #119 right after.
- **`http=000` means "no answer", never "no effect".** Four occurrences, all four
  had applied. Replay the **same** `requestId` — a 409
  `MOVE_WORK_REQUEST_ALREADY_APPLIED` confirms it landed. Never re-issue with a
  fresh key.
- **`you work list` is unusable against a large board.** It exceeds the CLI's
  ~10s timeout and then **exits 0 printing nothing** — an empty result means
  timeout, not an empty queue. Curl the endpoint. Queries degraded from 142s to
  ~308s as the board grew; the endpoint also returns malformed bodies under load
  and drops individual requests. One request at a time.
- **`gh pr list --state merged` sorts by created, not merged.** Reading row 1 as
  "newest merge" fabricates stalls. Use
  `--limit 100 -q 'sort_by(.mergedAt)|reverse'`.
- **The board monitor undercounts.** Twice it reported fewer FAILED tokens than
  an independent census (4 vs 8, 2 vs 3), because degraded reads were reported as
  clean ticks. Verify independently before acting.
- **`TaskStop` leaves the bash loop running.** Kill surviving PIDs by name and
  re-check; transient `curl` children die with the parent.
- **Codex quota is readable from disk**:
  `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` carries a `rate_limits` object
  (`used_percent`, `window_minutes`, `resets_at`, `credits`, `plan_type`) on
  `token_count` events. There is no `codex usage` command. Read the newest
  *populated* record — some events have the field present but all-null.

---

## 7. Constraints that stay in force

- Harness work goes to **port 7439** only. Never submit to **7437**
  (infinite-you) or **7467** (`factory-mv`) — both are other factories and were
  left running.
- The harness-root `credentials` file is **0 bytes** and must not be read. Live
  credentials come from `AGENT_MODEL__*` env vars or `~/.agent-cli/config.yaml`.
  No fixture may contain an API key or auth header.
- Never commit CI results, audit notes, or verification records to a branch —
  that creates a new head and invalidates the run being recorded. Evidence goes
  in a PR comment posted from a file (`gh api ... -F body=@file.md`), never
  inline with `--body "..."` (backticks get command-substituted).
- All test audio comes from local `qwen3-tts.cpp` only.
- Board state is in-memory; a daemon restart destroys it. Batches live in
  `C:/Users/andre/work/harness/batches/{s2s-program,s2s-localai}.json`.

---

## 8. How to regenerate this report

```bash
cd go-agent-harness
gh pr list --state all --limit 300 \
  --json number,state,headRefName,title,mergedAt,mergeStateStatus > prs.json
jq -r '.works[].name' ../batches/s2s-program.json ../batches/s2s-localai.json > lanes.txt
```

Join lane names against `headRefName` to get merged / open / never-started, and
walk the `relations` array (`DEPENDS_ON`, 187 edges) to recompute the frontier
and the per-PR gating counts. Lane titles — the operational description of each
work item — are at `.works[].payload.title`; the full assignment is in
`.works[].payload.mission`.

---


## 9. Vertical PR disposition triage (#165–#168) — 2026-08-25

Append-only section, recorded by the `s2s-vertical-pr-disposition-triage` lane.
All four open vertical scenario PRs were dispositioned against freshly fetched
`origin/main` = `b91bd65c0ce48bd20eaffb5a2dff9952bef33340` (2026-08-25) along the
three axes asked: outcome-on-main (blob-for-blob), textual-vs-semantic conflict
class, and keep-and-recut/close. Full reproducible evidence — commands, merge
bases, per-file blob hashes, merge-tree runs, check rollups with run URLs,
fixture-count ground truth, CI failure root causes — lives in the companion
appendix [`s2s-vertical-pr-disposition-triage-evidence.md`](s2s-vertical-pr-disposition-triage-evidence.md).
Per-PR actionable recut specs: `s2s-v2d-multi-utterance-recut-spec.md`,
`s2s-v6d-error-malformed-response-recut-spec.md`,
`s2s-v2e-audio-in-truncated-recut-spec.md`,
`s2s-v2c-audio-in-silence-recut-spec.md` (same directory). One disposition
comment was posted on each PR pointing here; this lane mutated nothing else on
the four PRs.

| PR | lane / outcome | outcome on main? (blob evidence) | conflict class + driver | recommendation | rationale anchor | spec / comment |
|----|----------------|----------------------------------|--------------------------|----------------|------------------|----------------|
| #165 | s2s-v2d-audio-in-multi-utterance | **No** — 5/6 files ABSENT_ON_MAIN; 6th DIVERGENT only in fixture exact-count constant; zero IDENTICAL blobs | **Textual**: 1 hunk, count `23` vs `22`; driver `bb85450` + `0e8184d` fixture reconciliation post-branch | **Keep and recut** → `s2s-v2d-multi-utterance-recut` (name unique 2026-08-25) | §3 self-contained-additions pattern; head checks green; no v2d artifact on main so the 2026-08-24 landed-outcome failure mode does not trigger | spec above; comment posted 2026-08-25 |
| #166 | s2s-v6d-error-malformed-response | **No** — scenario impl + both fixtures ABSENT_ON_MAIN; v6a auth-error scenario and generic malformed-frame fixture are related but are not this outcome | **Textual**: 2 hunks (`probe.go` insertion of main's `deriveToolResultObservation` from #170/#164 at the PR's registration site; shared count hunk); union resolves | **Keep and recut** → `s2s-v6d-error-malformed-recut` (unique) | §3 pattern; head checks green; no malformed-response vertical on main | spec above; comment posted 2026-08-25 |
| #167 | s2s-v2e-audio-in-truncated | **No** — all 8 new artifacts ABSENT_ON_MAIN; framework files DIVERGENT (two different expectation families added) | **Textual hunks at shared extension points; resolution is a semantic union** — 7 hunks in `probe.go`/`deadguard.go`/`expect.go`: #170's tool-result family vs the PR's `ExpectBufferDisposition` family; both must survive | **Keep and recut** → `s2s-v2e-audio-in-truncated-recut` (unique); red checks are inherited from stale base (count test fails identically at merge base: scanned 20 vs asserted 18) and vanish on rebase to main (green at 23) | §3 pattern; §4.7 no-force-push policy rules out fixing the orphaned branch in place; no truncated-audio artifact on main | spec above; comment posted 2026-08-25 |
| #168 | s2s-v2c-audio-in-silence | **No** — zero main-side commits to `session_audio_in.go` since merge base; silence/noise integration proof ABSENT_ON_MAIN | **None** — clean merge (`git merge-tree` exit 0; GitHub MERGEABLE) | **Keep and recut** → `s2s-v2c-audio-in-silence-recut` (unique); cheapest of the four — only blocker is gofmt drift in its own new test file (`make fmt-fix`) | §4.7 policy (fresh branch under an active lane instead of touching the orphaned head); alternative "land as-is with maintainer fmt fix" recorded in the spec | spec above; comment posted 2026-08-25 |

### Evidence appendix (key reproductions)

```bash
# fresh state
git fetch origin --prune && git rev-parse origin/main   # b91bd65c0ce48...

# per-PR metadata + checks
gh pr view <N> --json mergeStateStatus,mergeable,headRefOid,statusCheckRollup
# 165 DIRTY/CONFLICTING 2090fbd; ci+hermetic SUCCESS
# 166 DIRTY/CONFLICTING c61abdd; ci+hermetic SUCCESS
# 167 DIRTY/CONFLICTING 79a54e5; ci+hermetic FAILURE (inherited fixture-count)
# 168 UNSTABLE/MERGEABLE 266f220; ci+hermetic FAILURE (gofmt drift only)

# changed files over merge bases (165/167/168 -> 81267d6e; 166 -> b08c6da) with blob compare
MB=$(git merge-base origin/main <head>); git diff --name-only "$MB" <head>
git rev-parse <head>:<path>; git rev-parse origin/main:<path>   # exit code decides ABSENT_ON_MAIN

# real conflict enumeration (not GitHub labels)
git merge-tree --write-tree --name-only origin/main <head>
# 165 -> committed_fixtures_test.go x1 hunk
# 166 -> probe.go x1, committed_fixtures_test.go x1
# 167 -> probe.go x3, deadguard.go x1, expect.go x3
# 168 -> clean (exit 0)

# fixture-count ground truth (temp worktrees, go-llm-gateway module)
# 81267d6e FAIL scanned 20 want 18 | 79a54e5 FAIL identical | 2090fbd ok(22) | origin/main ok(23)

# branch-name uniqueness (all empty => unique, 2026-08-25)
git ls-remote origin refs/heads/s2s-v2d-multi-utterance-recut \
               refs/heads/s2s-v6d-error-malformed-recut \
               refs/heads/s2s-v2e-audio-in-truncated-recut \
               refs/heads/s2s-v2c-audio-in-silence-recut
```

Main-side churn attribution for every conflicted file:
`git log --oneline <merge-base>..origin/main -- <file>` → `0e8184d` (#170) everywhere,
plus `bb85450` on the fixture-count file. Current main vertical coverage for the
outcome-on-main axis: probe scenarios v1/v3b/error-auth only; integration tests
`session_audioin_live_test.go` + v3b fixtures only; session-fixtures dir has v6a auth
fixtures but no v6d malformed-response fixtures (full listing in the evidence appendix §7).
