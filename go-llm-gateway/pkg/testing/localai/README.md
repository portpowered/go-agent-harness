# LocalAI endpoint helper

`Prober.Endpoint` is intended for manual realtime checks against a local LocalAI fixture. It returns the exact URL it
attempted and probes for a `session.created` WebSocket event before reporting
that the server is ready.

```go
func TestLocalAIRealtime(t *testing.T) {
	wsURL, ok := localai.NewProber().Endpoint(t)
	if !ok {
		t.Fatalf("LocalAI realtime endpoint %s is unavailable; start it with: docker compose -f deploy/localai/docker-compose.yml up -d", wsURL)
	}

	// Use wsURL for the live protocol exchange.
}
```

The default endpoint is
`ws://localhost:8080/v1/realtime?model=gpt-realtime`. Set
`LOCALAI_REALTIME_URL` to replace that whole URL for a test or local fixture.
