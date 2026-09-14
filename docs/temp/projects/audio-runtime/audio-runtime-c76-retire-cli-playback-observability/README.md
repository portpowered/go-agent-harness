# C76 playback-observability evidence

This is the admitted evidence scope for
`audio-runtime-c76-retire-cli-playback-observability`. The existing
`go-agent-runtime/services/devices` contract and Wire root own the observer
composition; the CLI retains only compatibility adapters in the named legacy
file and the two admitted integration callers.

The standalone consumer is a separate module. Run it with:

```text
rtk proxy env GOWORK=off go test ./... -count=1 -timeout=180s
```

from `consumer/`. The bounded evidence runner can exercise the consumer,
focused observer tests, the room participant overflow regression, the shipped
audio-device-server process-boundary replay, and the existing C16
audio/tool replay:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c76-retire-cli-playback-observability/run.py --case all --child-timeout 60 --aggregate-timeout 600
```

`verify.py` contains independent literal projection, retirement, ancestry and
scope checks. Its mutation modes intentionally fail an altered oracle and only
return success when `--expect-failure` observes that failure.

The shipped process-boundary case is software loopback evidence. It uses the
production agent and `audio-device-server` path, but makes no native hardware
or acoustic claim and does not use Realtime or credentials. CI, independent
review, guarded merge, and the post-merge immutable vertical probe remain
external handoff gates.
