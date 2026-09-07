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
