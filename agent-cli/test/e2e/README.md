# Manual end-to-end tests

This package contains billed scenarios that cross process, browser, audio-device,
and provider boundaries. They are excluded from ordinary `go test ./...` runs.
Each scenario type has its own test file; common process dispatch lives in
`harness_test.go`.

Run the complete manual package:

```sh
go test -tags=e2e -count=1 ./agent-cli/test/e2e -v
```

Run only the browser workflow:

```sh
WEBMCP_PAPERIE_MARGIN_LIVE=1 \
WEBMCP_PAPERIE_MARGIN_LIVE_CDP_URL=http://127.0.0.1:9222/json/version \
go test -tags=e2e -count=1 ./agent-cli/test/e2e -run PaperieMargin -v
```

Run only the audio-device workflow:

```sh
WEBMCP_CUBECADE_AUDIO_DEVICE_LIVE=1 \
go test -tags=e2e -count=1 ./agent-cli/test/e2e -run CubecadeAudioDevice -v
```

Run the real `gpt-realtime-2.1` binary audio + tool round trip:

```sh
OPENAI_REALTIME_21_LIVE=1 \
AGENT_MODEL__OPENAI__API_KEY="$OPENAI_API_KEY" \
go test -tags=e2e -count=1 ./agent-cli/test/e2e -run GPTRealtime21 -v
```

Audit private EAC24–33 provider-edge captures without executing their tools:

```sh
EAC_CAPTURE_DIR=/absolute/path/to/captures \
go test -tags=e2e -count=1 ./agent-cli/test/e2e -run EAC24Through33 -v
```

Provider credentials retain their existing environment/file lookup behavior.
The `e2e_internal` tag is an implementation detail used by this package; invoke
manual scenarios through `agent-cli/test/e2e`.
