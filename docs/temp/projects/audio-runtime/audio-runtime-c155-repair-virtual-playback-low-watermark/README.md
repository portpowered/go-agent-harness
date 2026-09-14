# C155 virtual playback low-watermark repair

This evidence covers only the admitted software-device task
`audio-runtime-c155-repair-virtual-playback-low-watermark`.

## Admission and ancestry

- `project-control.py verify-work --type task --name audio-runtime-c155-repair-virtual-playback-low-watermark` returned `status: admitted` for `audio-runtime`.
- The isolated branch is `codex/audio-runtime-c155-repair-virtual-playback-low-watermark`, matching `prd.json`.
- Before mutation, `HEAD` and freshly fetched `origin/main` were `4a1c399ccbb3d780be95eb04316e84b8f11a6646`.
- The startup integration revision `8bdafc7f947a3a2c9856220abdc539437035bd21` is an ancestor of the branch.
- No prior C155 review work item or review findings were present in the canonical factory server list. The predecessor Windows finding is preserved below as the causal input.

## Causal finding

The predecessor’s Windows portable run `34780204709` on PR 497 reported:

```text
TestVirtualPlaybackCapacityAdversarial/08_waiter_remains_blocked_above_low_watermark
device_playback_adversarial_test.go:97: capacity wait returned while it should be blocked: <nil>
```

The accepted main branch did not reproduce that race in the ordinary local count-100, race count-50, or GOMAXPROCS 1/8 count-100 controls. A temporary, uncommitted start-late control forced the first read before starting the waiter and recorded:

```text
forced start-late control: waiter returned nil at queued=2400 above low=1920
```

That identifies the earliest incorrect ordering as waiter start after the first read, while the waiter is still unthrottled. It does not indicate a queue signaling, watermark, FIFO, or production-state defect.

## Repair

Subtest 08 now wraps `context.Background()` with an unexported test context whose standard `Done()` method closes a one-shot start channel. The channel is observed only after `WaitForPlaybackCapacity` has sampled the full queue, set its throttled state, captured the queue-change channel, and reached its blocking select. The paired reader starts only after that handshake. No private queue state or production instrumentation is exposed, and the production virtual-device implementation and watermark constants are unchanged.

The verbose causal run recorded:

```text
waiter-start queued=2880 low=1920 high=2880
read-signal queued=2880->2400
read-signal-final queued=2400->1920
waiter-return queued=1920
```

The first read remains above low and the waiter remains blocked; the final read reaches low and permits return.

## CI rejection repair

- Script CI run `34783651640` rejected head `51ddc49edeefc9f3fc318d96246e6d2f5065b92b` only in `CI (static)`, job `103795073680`. The complete job log reported four exact baseline drifts for `TestVirtualPlaybackCapacityAdversarial`: cyclomatic `52 > 49`, cognitive `106 > 101`, statements `222 > 206`, and physical lines `278 > 260`. Gofmt, generated Wire, vet, golangci-lint and Staticcheck all passed in that job. The raw job metadata is retained in `ci-rejection-34783651640.json`; no C155 review row or review finding exists.
- Repair commit `66aeb3b2bd787041149469bf2b56e5523d6dbb4f` keeps the legacy aggregate test at the exact admitted baseline metrics and factors only the deterministic waiter-start handshake into `startCapacityWaitAtBlocked`. The subtest name, start handshake, above-low blocked reads, low-watermark wake, timeout, and diagnostic start log remain unchanged in behavior. The architecture baseline and all unowned paths are untouched.
- After the repair, `make architecture-size-check` passes at `202` packages, `1940` files and `28759` functions. The causal subtest passes `200` normal, `100` race, `100` `GOMAXPROCS=1`, and `100` `GOMAXPROCS=8` repetitions. Adversarial normal/race passes are `900/420`; typed/loopback regressions pass `80`; device package normal/race passes `2530/759`; the full gateway module passes `285`; gateway vet, fmt, Wire, pinned lint, pinned Staticcheck and diff checks pass.
- Fresh `git fetch origin main` leaves `origin/main=4a1c399ccbb3d780be95eb04316e84b8f11a6646`; accepted main, startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, and the clean owned-path scope remain ancestors/intact. The final source checkpoint is `66aeb3b2bd787041149469bf2b56e5523d6dbb4f`.

## Verification

All commands below exited 0 on the candidate worktree:

- `go test ./go-device-gateway/pkg/devices -run '^TestVirtualPlaybackCapacityAdversarial/08_waiter_remains_blocked_above_low_watermark$' -count=100 -timeout=240s`
- `go test -race ./go-device-gateway/pkg/devices -run '^TestVirtualPlaybackCapacityAdversarial/08_waiter_remains_blocked_above_low_watermark$' -count=50 -timeout=420s`
- `GOMAXPROCS=1 go test ./go-device-gateway/pkg/devices -run '^TestVirtualPlaybackCapacityAdversarial/08_waiter_remains_blocked_above_low_watermark$' -count=100 -timeout=240s`
- `GOMAXPROCS=8 go test ./go-device-gateway/pkg/devices -run '^TestVirtualPlaybackCapacityAdversarial/08_waiter_remains_blocked_above_low_watermark$' -count=100 -timeout=240s`
- `go test ./go-device-gateway/pkg/devices -run '^TestVirtualPlaybackCapacityAdversarial/(04|05|06|07|08|09|10|11|12|13|14|15|16|17|18|19|20)' -count=50 -timeout=360s`
- `go test -race ./go-device-gateway/pkg/devices -run '^TestVirtualPlaybackCapacityAdversarial$' -count=20 -timeout=480s`
- `go test ./go-device-gateway/pkg/devices -run '^TestVirtualTypedPlayback(QueueIsBoundedAtResolvedRate|QueueMatchedRateDoesNotDrop|DiscardAndUnpairedStats)$|^TestVirtualLoopbackPreservesDelayBeyondPlaybackQueueCapacity$' -count=20 -timeout=300s`
- `go test ./go-device-gateway/pkg/devices -count=10 -timeout=420s`
- `go test -race ./go-device-gateway/pkg/devices -count=3 -timeout=600s`
- `go test ./go-device-gateway/... -count=1 -timeout=600s`
- `go vet ./go-device-gateway/...`
- `git diff --check`

The implementation checkpoint, PR, and script-CI handoff boundary are recorded in `progress.txt`; the final pushed handoff head is supplied in the task response.

## Latest current-main integration

- Fresh `origin/main` is `2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`, merged into
  this same branch by non-ff commit `9e08472a0`; accepted main and startup
  integration remain ancestors. The host checkout and peer worktrees were not
  reset or modified.
- The shared-root C155 progress append was removed; C155 evidence remains under
  this owned directory. The executable diff remains limited to the adversarial
  test; production virtual-device code and unowned paths are unchanged.
- Post-merge causal normal/race/GOMAXPROCS checks passed `200/100/100/100`,
  adversarial normal/race passed `900/420`, typed/loopback passed `80`, the
  gateway module passed `285`, device-package race passed `759`, and gateway
  vet passed. Architecture-size passed at `202/1941/28785`; fmt, Wire,
  pinned Staticcheck, pinned golangci-lint and diff-check passed, with lint at
  `0 issues` in every module.
- This remains executor evidence only. Script CI, independent review, guarded
  merge, vertical acceptance and project completion are still open.

## Current-main integration and recheck

- The canonical task is `work-task-46`; concluded review `work-review-68` found no C155 code defect. It rejected the prior handoff only because `origin/main` had advanced to `97d3dcfb1e97a2611aa26b203a7f893442db4768` while the candidate stopped at `4a1c399ccbb3d780be95eb04316e84b8f11a6646`. The exact prior static rejection remains `34783651640`/`51ddc49edeefc9f3fc318d96246e6d2f5065b92b` with the four baseline drifts recorded above.
- `git fetch origin main` confirmed `origin/main=97d3dcfb1e97a2611aa26b203a7f893442db4768`. The isolated branch merged it as `1caea9e119f9396b8110a2847e463ee6270c2714`; merge-base is the planned `4a1c399ccbb3d780be95eb04316e84b8f11a6646`, and startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21` remains an ancestor. The running host checkout and peer worktrees were not reset or modified.
- On merged source `1caea9e1`, the causal subtest passed `200` normal, `100` race, `100` `GOMAXPROCS=1`, and `100` `GOMAXPROCS=8` trials; adversarial normal/race passed `900/420`, typed/loopback regressions passed `80`, the device package passed `2530` normal and `759` race tests, the full gateway module passed `285`, and gateway vet passed. `make architecture-size-check` passed at `202` packages, `1940` files and `28768` functions; `make fmt`, `make wire-check` and `git diff --check` passed.
- This refresh is executor evidence only: no script-CI success, independent review, guarded merge, vertical acceptance, physical/acoustic proof or project completion is claimed. The evidence-only descendant changes no executable inputs. Next action is to push this same admitted branch, update its PR with the current-main and focused evidence, and return `ACCEPTED` to the script-owned CI gate without polling; retain C155 ownership for any exact rejection.

## Review-96 causal oracle repair

- The authoritative `work-review-96` finding, repeated on `work-review-104`, was
  repaired on source commit `47189d1564003795762c5ab7946347af0ea729a3`. The
  initial waiter-start handshake was insufficient because a post-read waiter
  could return before the nonblocking assertion ran. The owned test now makes
  each above-low read wait for the waiter to re-sample and reach its next
  `Done()` boundary, fails immediately if the waiter returns or does not
  re-block, and retains the nonblocking blocked assertion after the barrier.
- The same behavior-level helpers restore ordered diagnostics and threshold
  assertions without touching production or the architecture baseline:
  `waiter-start queued=2880 low=1920 high=2880`,
  `read-signal queued=2880->2400`, `waiter-reblock queued=2400`,
  `read-signal-final queued=2400->1920`, and
  `waiter-return queued=1920`. The final return is accepted only at or below
  low with the incoming frame still fitting under high.
- Post-repair evidence is green: the exact causal subtest passes 200 normal,
  100 race, and 100 each at `GOMAXPROCS=1/8`; adversarial normal/race passes
  are `900/420`; typed/loopback regressions pass `80`; device-package normal
  and race pass `2530/759`; the full gateway module passes `285`; and gateway
  vet passes. `make architecture-size-check` remains green at `202/1941/28788`.
  Formatting, Wire, pinned golangci-lint `2.9.0` (0 issues), pinned
  Staticcheck `2026.1`, and `git diff --check` pass.
- Fresh `origin/main` remains
  `2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`; accepted main, startup
  integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, and the owned
  source scope remain intact. No C155 review approval, guarded merge,
  vertical acceptance, hardware/acoustic proof, or project completion is
  claimed.

Next bounded action: record this evidence checkpoint, push the same admitted
branch, update PR `#516` with the exact repair head and prior finding mapping,
then return `ACCEPTED` to the script-owned current-head CI gate without
polling. Retain C155 ownership through `CONTINUE` for any exact CI rejection
or actionable review finding.
