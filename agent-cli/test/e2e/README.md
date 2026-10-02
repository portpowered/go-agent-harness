# Billed end-to-end tests

This package holds the billed `gpt-realtime-2.1` round trip through the compiled
CLI binary. It builds only with the `live` tag (credentialed or billed tests) and
is excluded from ordinary `go test ./...` runs:

```sh
AGENT_MODEL__OPENAI__API_KEY="$OPENAI_API_KEY" \
go test -tags=live -count=1 ./agent-cli/test/e2e -run GPTRealtime21 -v
```

The other billed scenarios live beside the code they exercise and also build
with `live`:

```sh
# Cubecade page driven by the agent binary through the remote audio device.
go test -tags=live -count=1 ./agent-cli/internal/webmcp/chrome \
  -run '^TestPinnedChromeCubecadeAgentUsesAudioDeviceServer$' -v

# Paperie and Margin multi-page browser tool use.
WEBMCP_PAPERIE_MARGIN_LIVE_CDP_URL=http://127.0.0.1:9222/json/version \
go test -tags=live -count=1 ./agent-cli/internal/transport/cli \
  -run '^TestSessionPaperieMarginFromBaselineAgentsMD$' -v
```

Provider credentials keep their existing environment and file lookup.
