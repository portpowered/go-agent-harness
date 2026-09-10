# C42 implementation handoff

This is the implementation handoff for
`audio-runtime-c42-capture-energy-software-coverage`. It records the software
candidate evidence that is ready for the script CI gate; it does not assert
that CI is green and it does not replace independent review or post-merge
probe acceptance.

## Candidate scope

- Codec coverage is test-only in the existing `sample_value_test.go`; the
  current production `PacketEnergy` behavior already satisfies the typed,
  bounded semantics demonstrated by the new tests.
- Windows coverage adds only direct `wasapiCapturePacketEnergy` test entry-point
  coverage in the existing Windows test file. The production path is verified
  to delegate to `codec.PacketEnergy`.
- The separate consumer module and `run.py` are confined to this evidence
  directory. `go.work`, root module files, workflow files, and policy files are
  untouched.
- The original C32 expected oracle and original failed vertical report are
  retained byte-for-byte, with the failed-report hash pinned to
  `0345a628a6038e18bf7c7a59016e7a0002ae89734c6ff59aa541642f34b3d960`. No
  acceptance waiver is requested.

## Local gate evidence

The positive software run passed with 53 consumer cases, one independently
validated finite single-square control (`0x1p+511` squared to `0x1p+1022`), and
exact fixture outputs. It also passed the capped output and bounded
SIGKILL-reap controls, credential-free audio/tool and interruption fixture
replay, clean timeline checks, exact marker/provider/rendered PCM hashes, and
strict directory replay. The negative run passed by rejecting a wrong C42
energy oracle and rejecting a bundle with `audio-trace/timeline.jsonl` removed.
The reproducible commands and frozen input hashes are in `README.md`,
`coverage-map.md`, and the generated `verification-report.json`.

Focused Go evidence already collected:

```text
go test ./go-audio/pkg/codec -count=1 -timeout=60s                 PASS
go test -race ./go-audio/pkg/codec -count=1 -timeout=60s           PASS
GOOS=windows GOARCH=amd64 go test -c ./go-device-gateway/pkg/devices PASS (PE32+)
go test ./go-device-gateway/pkg/devices -run '^$' -count=1        PASS (non-Windows compile gate)
```

The Windows binary was cross-compiled locally. The prior exact-head script-CI
candidate also has native Windows software evidence:

```text
workflow: .github/workflows/ci.yml
run: https://github.com/portpowered/go-agent-harness/actions/runs/34487167972
job: https://github.com/portpowered/go-agent-harness/actions/runs/34487167972/job/102904301028
head: d301084027c8669f4af5b15c86a77cb847e21d6b
platform: Windows/amd64
command: go test ./go-audio/... ./go-device-gateway/... -run 'TestWindowsPortablePlaybackBurstPreservesFIFO|TestVirtualPlaybackCapacityAdversarial' -count=1
result: SUCCESS on the prior candidate head; the repaired head requires a new script-CI run
```

The new review repairs change the candidate after that run, so this historical
green result is not relabeled as current-head CI. Native Windows endpoint use
and physical/acoustic testing are `OUT OF SCOPE` under the effective user scope
amendment; they are neither a PASS claim nor a prerequisite for this software
delivery.

## Scope amendment and remaining gates

Amendment provenance is `factory/docs/operating-policy.md`, section `User scope
amendment — 2026-09-10`. It keeps Windows software execution/compilation and
hermetic CI in scope while excluding native Windows hardware/endpoints and
physical acoustic testing. The primary must bind any later C42 report to this
effective amendment without rewriting the historical C32 report.

Remaining scoped gates are `AUDIO`, `DEVICE` (software selection and adapter
coverage only), `QUALITY`, and `PARITY`, plus the changed-head script-CI gate,
independent review, guarded merge, and the primary's fresh post-merge vertical
probe. No CI, review, merge, vertical acceptance, or project completion is
claimed here.

## Handoff action

After committing and pushing this same task branch, open or update the task PR
with the exact candidate revision and this evidence directory. Submit the
candidate to script CI. If CI rejects it, inspect only the exact failed checks,
repair the same task, rerun the focused causal evidence, and resubmit; do not
poll CI or duplicate the full suite locally. Independent review remains the
next authority after script CI.
