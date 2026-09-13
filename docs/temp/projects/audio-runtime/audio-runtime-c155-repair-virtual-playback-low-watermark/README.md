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

The exact pushed revision, PR, and script-CI handoff are recorded in `progress.txt` after commit.
