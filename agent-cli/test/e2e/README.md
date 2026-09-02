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

Provider credentials retain their existing environment/file lookup behavior.
The `e2e_internal` tag is an implementation detail used by this package; invoke
manual scenarios through `agent-cli/test/e2e`.
