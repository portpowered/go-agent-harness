# Realtime tier conformance suite

The live LocalAI and OpenAI conformance suite was removed: no CI job could
run it. What remains are the offline assertion controls, which prove the
response assertions reject silent PCM, withheld history, missing tools, and
missing images:

```powershell
$env:GOWORK = "off"
go test ./... -count=1
```

The suite keeps its own small PCM16 helpers (`resamplePCM16`, `pcm16RMS` in
`protocol_test.go`) on purpose. It is a standalone module (`GOWORK=off`) that
talks raw Realtime WebSocket events and depends on nothing in this
repository, so its oracle stays independent of the go-audio code under test.
It is therefore outside the architecture gate's `hand-rolled-pcm16` and
`hand-rolled-wav-container` source-pattern rules, which govern the workspace
modules. Note that `pcm16RMS` normalizes by `math.MaxInt16` (32767) where
`go-audio/pkg/codec` normalizes by 32768; the difference is below the
`silenceRMSThreshold` margin these checks use.

The dated measurement boundary is maintained in
`docs/architecture/s2s-local-tier-conformance.md`.
