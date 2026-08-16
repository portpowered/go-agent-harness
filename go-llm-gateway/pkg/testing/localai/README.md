# LocalAI endpoint helper

`Endpoint` is intended for opt-in realtime tests. It returns the exact URL it
attempted and probes for a `session.created` WebSocket event before reporting
that the server is ready.

```go
func TestLocalAIRealtime(t *testing.T) {
	wsURL, ok := localai.Endpoint(t)
	if !ok {
		t.Skipf("LocalAI realtime endpoint %s is unavailable; start it with: docker compose -f deploy/localai/docker-compose.yml up -d", wsURL)
	}

	// Use wsURL for the live protocol exchange.
}
```

The default endpoint is
`ws://localhost:8080/v1/realtime?model=gpt-realtime`. Set
`LOCALAI_REALTIME_URL` to replace that whole URL for a test or local fixture.

## Live audio proof

`TestLocalAIRealtimeAudio` uses the helper above and skips when the endpoint is
absent. When the fixture is running, it opens a second raw WebSocket, waits for
`session.created`, declares 16 kHz input and PCM16 output, appends a
deterministic PCM16 utterance in 100 ms chunks, commits it, and sends
the LocalAI turn. The utterance is checked in as mono 16 kHz PCM16, so the
test never invokes a TTS binary. It base64-decodes
`response.output_audio.delta`, computes normalized little-endian PCM16 RMS,
and requires RMS above `0.01`; a socket that accepts TCP but never completes
the WebSocket protocol fails within the test deadline.

The skipped test names the exact attempted endpoint and start command:

```text
docker compose -f deploy/localai/docker-compose.yml up -d
```
