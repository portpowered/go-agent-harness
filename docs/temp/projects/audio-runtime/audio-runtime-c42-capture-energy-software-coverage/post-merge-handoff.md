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
  retained byte-for-byte. No acceptance waiver is requested.

## Local gate evidence

The positive software run passed with 53 consumer cases and exact fixture
outputs. It also passed the capped output and bounded SIGKILL-reap controls,
credential-free audio/tool and interruption fixture replay, clean timeline
checks, exact marker/provider/rendered PCM hashes, and strict directory replay.
The negative run passed by rejecting a wrong C42 energy oracle and rejecting a
bundle with `audio-trace/timeline.jsonl` removed. The reproducible commands and
frozen input hashes are in `README.md`, `coverage-map.md`, and the generated
`verification-report.json`.

Focused Go evidence already collected:

```text
go test ./go-audio/pkg/codec -count=1 -timeout=60s                 PASS
go test -race ./go-audio/pkg/codec -count=1 -timeout=60s           PASS
GOOS=windows GOARCH=amd64 go test -c ./go-device-gateway/pkg/devices PASS (PE32+)
go test ./go-device-gateway/pkg/devices -run '^$' -count=1        PASS (non-Windows compile gate)
```

The Windows binary was cross-compiled only; native Windows execution and
physical/acoustic endpoint consumption remain explicitly unclaimed. That
boundary is preserved from the original C32 failed vertical report and is a
prerequisite for a later native acceptance probe.

## Handoff action

After committing and pushing this same task branch, open or update the task PR
with the exact candidate revision and this evidence directory. Submit the
candidate to script CI. If CI rejects it, inspect only the exact failed checks,
repair the same task, rerun the focused causal evidence, and resubmit; do not
poll CI or duplicate the full suite locally. Independent review remains the
next authority after script CI.
