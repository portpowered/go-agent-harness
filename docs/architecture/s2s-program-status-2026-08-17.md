# s2s program — status and handoff, 2026-08-17 12:00Z

Written at operator shutdown. The factory daemon, the board monitor, and the
hourly backstop cron were all terminated at this point. This document is the
resume point.

---

## 1. Stop reason — read this first

**The codex weekly rate limit is exhausted.**

| field | value |
|---|---|
| `used_percent` | **100.0** (was 86% at 03:55Z) |
| `window_minutes` | 10080 (7 days) |
| `credits` | `balance: "0"`, `has_credits: false` |
| `plan_type` | `pro` |
| `resets_at` | **2026-08-20 03:31:14Z** (~64h from shutdown) |

No lane can make progress until this resets or credits are added. Everything
below is blocked on that, not on the board.

---

## 2. Where the program stands

| bucket | count |
|---|---|
| Lanes **complete** (merged + consumed) | **87** |
| Lanes **in flight** (PR open, unmerged) | **17** |
| Lanes **not started** (`init/INITIAL`) | **58** |
| Merged PRs on `main` | 122 |

Board went 76 → 87 complete over the shutdown session. All merged work is on
`main` and is safe — it does not depend on the factory's in-memory state.

---

## 3. In-flight lanes — the 17, with actual blockers

Reviewers posted specific, actionable feedback on all of these. Grouped by
what is actually stopping them, because the groups need different fixes.

### 3a. Real, fixable defects (lane can finish unaided)

| PR | lane | blocker |
|---|---|---|
| #46 | `s2s-b1-cov-session-storage` | same-ID concurrent writes **reproducibly corrupt JSON**; `make test` fails; CI terminal red |
| #116 | `s2s-b3-devices-list-command` | production CLI still reports `unknown command` |
| #118 | `s2s-b2-transport-grok-retype` | consumer migration incomplete — **41 comments, 19 review passes**, pathological churn |

### 3b. Blocked on production contracts OUTSIDE the lane's lease

These cannot be fixed by the lane. They need an operator decision (§5.1).

| PR | lane | needs |
|---|---|---|
| #97 | `s2s-b3-session-image-input` | production OpenAI realtime session lacks the complete-message contract |
| #113 | `s2s-b2-audio-device-source-and-sink` | real WASAPI frame I/O in `device_windows.go` + shared S11 conformance infra |
| #110 | `s2s-b3-session-recording-flags` | production seam owned by PR #98, a *different* lane, with no `DEPENDS_ON` edge |
| #114 | `s2s-b1-cov-services-chat-core` | typed command-error assertions, at-file path-escape, bounded scrollback — all production APIs |

### 3c. Impossible on this host

| PR | lane | why |
|---|---|---|
| #102 | `s2s-b2-device-macos-coreaudio` | requires `TestCoreAudioDeviceRegistryHardware` on **native macOS with real CoreAudio hardware and mic permission**. This is a Windows workstation. No amount of factory throughput satisfies it. |

### 3d. Remaining open, less diagnosed

`#77` hermetic-test-target, `#83` session-tool-executor-wiring, `#98`
session-audio-in-file, `#107` lai-local-tier-conformance, `#68`
lai-realtime-client-lib-decision, `#132` rtc-audio-track-egress, `#133`
rtc-audio-track-ingress, `#138` repair-rtc-opus-codec-owner.

### 3e. Stale branches — need a rebase, force-push required

Never authorised, so never done. Depth behind `origin/main` at shutdown:

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

The top three have drifted a further ~60-90 commits during the session —
they are not rebasing on their own.

---

## 4. The 58 not-started lanes

Untouched, still `init/INITIAL`. Whole groups, so scope is easy to reason about:

- **`s2s-b4-v*` — 30 verification lanes.** The actual audio/tool/error matrix:
  text-in-audio-out, audio-in (basic/long/silence/multi-utterance/truncated),
  barge-in ×3, tool calls ×6, toolsets ×3, errors ×4, metrics ×3, duplex
  overlap, webrtc device roundtrip and external source.
- **`s2s-e2e-*` — 6 lanes.** audio roundtrip proof, multiturn conversation,
  tool-call conversation, vision describe, conversation observability,
  customer acceptance probe.
- **`s2s-acc-*` — 4 lanes.** probe goal catalog, stuck detection, friction
  report, fleet gate.
- **`s2s-b3-probe-*` — 4 lanes**, `s2s-b4-rtc-*` — 4, `s2s-b3-session-*` — 6,
  plus `s2s-b2-record-fixtures`, `s2s-b2-transport-recordreplay-retype`,
  `s2s-b4-fault-injection`, `s2s-b4-fleet-*`, `s2s-lai-blind-probe-local-tier`.

**Note:** the acceptance criterion for this program — blind probes driving the
CLI to call a tool and describe an image, with all probes reporting it was easy
and it worked — lives in `s2s-acc-*` + `s2s-e2e-customer-acceptance-probe`.
**None of it has run yet.** The 87 completed lanes are foundation: coverage,
splits, transport, devices, transcripts, parity, clock, RTC primitives.

---

## 5. Decisions required before resuming

### 5.1 Lease-vs-criteria conflict — the main one

Coverage/test lanes were given acceptance criteria requiring production
contracts they are forbidden to write, with no dependency edge to the lane that
owns them. Reviewers correctly refuse to merge; lanes correctly cannot comply.
Options:

1. **File the missing production contracts as their own lanes** and gate the
   coverage lanes behind them with `DEPENDS_ON`. Most faithful to the testing
   bar. Adds lanes to an already-58-deep queue. *(Recommended.)*
2. **Widen the leases** so coverage lanes write the production API themselves.
   Faster, breaks surface ownership, invites cross-lane conflict.
3. **Descope those criteria.** Cheapest, directly lowers the testing bar.

### 5.2 The macOS hardware criteria (#102)

Cannot be satisfied on this host under any option above. Either descope to
non-hardware assertions, or run that lane on a Mac.

### 5.3 Stale-branch rebase

Requires force-push. Never authorised.

### 5.4 Whether to keep paying for #118

19 review passes, 41 comments, still incomplete. Consider cancelling and
re-cutting it as a fresh, smaller lane.

---

## 6. Infrastructure fixed during the session (all on `main`)

| commit | what |
|---|---|
| `a868f31`, `685075e` | **ideafy `--server` fix.** Its queue-reconciliation commands omitted `--server` and silently hit port 7437 (infinite-you), so every self-heal returned `FACTORY_UNREACHABLE` while appearing to run. This is why the queue never recovered on its own. |
| `e17e1f6` | **`process/AGENTS.md` 17.4/17.5** — lease exhaustion is completion. Fixes the `<CONTINUE>` deadlock (§7.1). |
| `a5a41f3` | **17.4.1** — 17.4 is not an exit from your own assignment; `git diff origin/main...HEAD` must be non-empty before invoking it. |
| `19f0d0f` | **Lane sizing bound counts production lines only.** Test/fixture/testdata/golden lines exempt. |
| `4161ca2`, `34f8b2e` | Rules doc §9 (LocalAI tier + licence) and §10 (the deadlock, recorded). |

---

## 7. Operational knowledge — do not rediscover this

### 7.1 The `<CONTINUE>` deadlock (cost ~17 hours)

`review` is a **two-token join**: it consumes `task:in-review` **and**
`review:init`. `process` is a REPEATER that mints that pair **only** on
`<COMPLETE>`; `<CONTINUE>` re-arms it and fires no output arc.

> A lane emitting `<CONTINUE>` is not queued for review. It has made review
> **structurally unreachable**, and its PR can never merge however green it is.

Lane `s2s-b1-cov-workspace-agents-md` did this for ~17 hours against PR #58
(`OPEN`/`MERGEABLE`/`CLEAN`, CI green throughout). 16 of 19 open lanes were in
the same state. **#58 merged 9 minutes after the lane was told to stop.**

### 7.2 Never hand-move a token to `task:in-review`

It looks like a shortcut to review and is a dead end — the paired `review:init`
token does not exist, so the join can never fire, and the only transition that
can consume it is `review-loop-breaker`, which fails it. **`init` is the only
safe manual destination.** Verified by experiment.

### 7.3 Stranded ideas — the failure the FAILED count hides

An idea at `to-complete/PROCESSING` with **no task token at all** contributes
**zero** to the FAILED count while being permanently dead: `consume` joins
`idea:to-complete` with `task:to-complete` by SAME_NAME, and there is no token
to move. Remedy: move the **idea id** to `init`. Hit 3 lanes; confirmed working
(`s2s-b2-wire-mock-injection` merged as #119 afterwards).

### 7.4 `http=000` means "no answer", never "no effect"

Four occurrences this session, **all four had applied**. Correct response is an
idempotent replay of the **same** `requestId` — a 409
`MOVE_WORK_REQUEST_ALREADY_APPLIED` confirms the original landed. Never re-issue
with a fresh key.

### 7.5 Querying the board

`you work list` costs ~20s fixed against this server, exceeds the CLI's ~10s
timeout, and then **exits 0 printing nothing** — an empty result means timeout,
not an empty queue. Always curl the endpoint directly. Board queries degraded
from 142s to ~308s over the session as the board grew; the endpoint also
returns malformed bodies under load (`unparseable board response`), and drops
individual requests. Run one request at a time.

### 7.6 `gh pr list --state merged` sorts by **created**, not merged

Reading row 1 as "newest merge" is wrong and will fabricate stalls. Use
`--limit 100 -q 'sort_by(.mergedAt)|reverse'`.

### 7.7 The monitor undercounts

Twice it reported fewer FAILED tokens than an independent census (4 vs 8, 2 vs
3), because its board fetches were degraded reads that it then reported as a
clean tick. Always verify independently before acting.

---

## 8. How to resume

1. Wait for quota reset (**2026-08-20 03:31Z**) or add codex credits.
2. Make the §5 decisions — especially 5.1, which blocks 4+ lanes outright.
3. Restart the factory daemon on **port 7439** (not 7437 — that is
   infinite-you and must never receive harness work).
4. Board state was **in-memory and is gone.** Resubmit from the batch files:
   - `C:/Users/andre/work/harness/batches/s2s-program.json`
   - `C:/Users/andre/work/harness/batches/s2s-localai.json`
   Already-merged lanes are on `main`; re-running them is wasted quota, so
   prune the batch to the 17 in-flight + 58 not-started lanes before submitting.
5. The `process/AGENTS.md` rules (17.4/17.4.1/17.5) are on `main` and apply
   automatically to every future lane — no per-worktree notes needed.

## 9. Constraints that stay in force

- Harness work goes to **7439** only. Never submit to 7437 (infinite-you).
- The harness-root `credentials` file is **0 bytes** and must not be read.
  Live credentials come from `AGENT_MODEL__*` env vars or
  `~/.agent-cli/config.yaml`. No fixture may contain an API key or auth header.
- Never commit CI results, audit notes, or verification records to a branch —
  that creates a new head and invalidates the run being recorded. Evidence goes
  in a PR comment, posted from a file (`gh api ... -F body=@file.md`), never
  inline with `--body "..."` (backticks get command-substituted).
- All test audio comes from local `qwen3-tts.cpp` only.
