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

The five source runs exposed two lifecycle races and one fixture ordering bug:

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

On macOS, the cumulative cohort passed three complete iterations in normal and
coverage modes and three complete race iterations. Each mode included 60 high-rate
audio trials. The exact-marker unit checks passed 100 repetitions; the direct and
suite Family B checks passed 30 repetitions each. Earlier diagnostic runs removed
the 30-second Family B hang but still saw fast normalized-transcript timestamp
ordering failures in 3/50 and 4/100 suite repetitions even though provider-local
events were ordered. Keep that recorder-ordering observation visible when triaging
future hosted failures; the matcher does not weaken the logical-clock assertions.

The Family B fixture was decomposed from one 801-line file into files of 275, 120,
211, and 226 lines. Total fixture code grew slightly because each focused file has
its own package/import structure and the marker received dedicated tests. The
largest file fell by 526 lines (65.7%). Live event translation is isolated in the
`eventcodec` package, continuation lifecycle code is grouped in `continuation.go`,
and replay fixture/path checks share one coherent test file. `make size-check`
passes with lower legacy baselines; no size or complexity threshold was raised.
