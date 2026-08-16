# Realtime tier conformance suite

This optional suite runs the same five behavior bodies against LocalAI and
OpenAI: audio round trip, retained three-turn context, VAD/barge-in, a
model-chosen function call, and image input. It uses raw OpenAI-compatible
Realtime WebSocket events so the assertions observe customer-visible output
instead of provider-specific gateway internals.

Run it from this directory because the repository is a Go workspace with no
root module:

```powershell
$env:GOWORK = "off"
go test ./... -count=1 -timeout 120s
```

The LocalAI case uses `LOCALAI_REALTIME_URL`, then
`AGENT_MODEL__LOCALAI__BASE_URL`, then the pinned fixture default. The OpenAI
case requires `AGENT_MODEL__OPENAI__API_KEY` and always requests
`gpt-realtime-2.1-mini`; `AGENT_MODEL__OPENAI__BASE_URL` may override the
WebSocket endpoint. Secret values are never printed and the suite never reads
the repository `credentials` file.

Missing LocalAI or OpenAI prerequisites are named skips. A reachable endpoint
that fails a behavior is a test failure. The four assertion controls run
without live services and intentionally log their expected rejection: silent
PCM, withheld history, no tools, and no image.

The dated measurement boundary is maintained in
`docs/architecture/s2s-local-tier-conformance.md`.
