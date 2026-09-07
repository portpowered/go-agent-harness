# Reproducing recurring session CI failures

From the repository root:

```sh
scripts/test-session-ci-regressions.sh normal
scripts/test-session-ci-regressions.sh coverage
scripts/test-session-ci-regressions.sh race
# Or run all three modes, preserving failures from every mode:
scripts/test-session-ci-regressions.sh all
```

The runner selects the cumulative top-level failing tests from GitHub Actions runs
34121252743, 34128356822, 34132278337, 34134396539, and 34137090509. All their
subtests run, including the high-rate audio stress trials. The CLI interrupt-order
case lives in a separate package, so the runner checks both packages sequentially.
It does not discard an earlier failure when a later package succeeds.

Each mode repeats the cohort three times with the microphone stub. Normal and
coverage use CGO disabled; race enables CGO for Go's race detector. Coverage enables
instrumentation; it does not measure the repository's coverage threshold. Every
package invocation retains the existing eight-minute timeout and shorter test
or child-process deadlines. The tests use fixtures/loopback providers, not live
Realtime sessions. Run with the repository's Go version (CI uses Go 1.26.7).

Use `COUNT=1` for diagnosis or a larger count for repeated evidence. Each mode
and package is printed before execution, and verbose Go output names failing
scenarios. Save stdout/stderr with normal shell redirection when attaching evidence.
Keep the exit status: a failure must never become success through a log pipeline.

The cohort is a compact reproduction target, not a substitute for full CI. Ubuntu
CI and macOS can expose different scheduling; record OS, source revision, mode,
count and outcome. A local pass does not establish hosted CI success. Add newly
identified recurring scenarios by name, retaining the meaningful negative controls.

## Stabilization findings

The five source runs exposed a continuation-admission race and two fixture
ordering problems:

- Tool results and provider responses cross separate queues. The live adapter now
  records a tool-result or continuation admission before calling a provider that
  may respond synchronously, then rolls back only that admission if the send is
  rejected. A failed continuation received before the local tool `MESSAGE.END` is
  held until that accepted result boundary arrives, preserving the typed image or
  tool continuation failure.
- The tool-filter fixture used to close its provider immediately after sending the
  continuation. It now closes after the observer has received every expected tool
  result, so the test measures filtering and result correlation deterministically.
  This barrier does not prove that arbitrary output survives an abrupt provider
  close; transport teardown durability remains a separate contract.
- Family B gated correction input on a total stdout byte count that included a
  separately suppressible tool-continuation audio marker. It now waits for the
  exact original-response marker, across split reads, with bounded retained output
  and explicit cancellation/closed-output errors. Response identity, tool order,
  correction timing, filesystem checkpoints, and both audio markers remain
  independent assertions, so a coincidental PCM sequence cannot satisfy the test.
- The high-rate remote-device oracle counted every nonzero rendered sample as
  provider PCM. On slower hosted trials, the default 2.5-second local hold-tone
  policy emitted cue samples during tool gaps, producing an apparent 105.9%
  retention despite zero drops, overflows, or discards. Exact-delivery fixtures
  now select an explicit one-hour cue threshold through the embedded session and
  runtime-device request. The production nil policy still uses the default cue.
  A forced three-second gap control proves that the default emits a bounded
  bipolar cue between two unchanged provider responses, while the provider-only
  fixture preserves strict full-slice equality. Duplication, loss, reorder, and
  corruption negative controls remain strict.

On macOS, the cumulative cohort passed three complete iterations in normal and
coverage modes and three complete race iterations. Each mode included 60 high-rate
audio trials. The exact-marker unit checks passed 100 repetitions; the direct and
suite Family B checks passed 30 repetitions each. Hosted run 34151678218 later
passed the high-rate suite but reproduced one Family B recording-order failure in
the coverage job. Exact local coverage repetitions reproduced four failures in 200
runs: stdout had already delivered the original PCM marker, while the normalized
MESSAGE.START/audio observation was recorded after RESPONSE.CANCEL and the
correction input.

The media bridge now finishes bounded recorder admission before it makes each
frame readable to the device. A device reader waiting for recorder admission
remains cancellable, and a final frame whose observation completed still drains
after concurrent port closure. This establishes the device/recording order without
sorting timestamps; device delivery waits for that bounded recorder admission.
It does not yet identify an untagged bridged frame with its provider response. The
Family B fixture omits provider item IDs; adding them exposed a second real gap:
server-VAD interruption can discard the queued response-end marker after a frame
has reached the downstream finite processor, so the next tagged response can fail
with `ErrStreamIdentityChanged`. The ambiguous parser and tagged-fixture
experiments were removed. Family B response correlation and interruption reset
propagation remain explicit follow-up work; the exact marker does not weaken the
logical-clock assertions.

The Family B fixture was decomposed from one 801-line file into files of 275, 120,
211, and 231 lines. Total fixture code grew slightly because each focused file has
its own package/import structure and the marker received dedicated tests. The
largest file fell by 526 lines (65.7%). Live event translation is isolated in the
`eventcodec` package, continuation lifecycle code is grouped in `continuation.go`,
and replay fixture/path checks share one coherent test file. `make size-check`
passes with lower legacy baselines; no size or complexity threshold was raised.
The remote tool-audio fixture moved its oracle and damage controls into a focused
207-line file and deleted an 89-line ad-hoc test that always skipped. Its main
scenario file fell from 1,154 to 1,048 lines; `runRemoteToolAudioScenario` fell
from 58 to 56 cyclomatic complexity, 61 to 59 cognitive complexity, 132 to 128
statements, and 195 to 186 lines.
