# WebMCP Gate I2 realtime measurement

Gate I2 is an explicit, credentialed measurement of the production session
command. Its live execution is not part of the default test, lint, or CI path.
The test starts
the locked Chrome for Testing build, serves the declarative fixture from
`agent-cli/internal/webmcp/chrome/testdata/webmcp_adapter.html`, builds the
production `agent` binary, and sends one finite spoken request to OpenAI
Realtime.

## Run

Run this from the repository root on the qualified `darwin/arm64` host:

```sh
export WEBMCP_GATE_I2=1
export OPENAI_API_KEY_FILE=/secure/path/openai-api-key
export WEBMCP_GATE_I2_ARTIFACT_DIR="$PWD/.artifacts/webmcp-gate-i2"
go test -tags live ./agent-cli/internal/webmcp/chrome \
  -run '^TestPinnedChromeOpenAIRealtimeWebMCPGateI2$' -count=1 -v
```

`OPENAI_API_KEY_FILE` is preferred. The runner reads it through the documented
newline-stripping protocol (`tr -d '\r\n'`), passes the resulting value only
through the child process environment, and never logs or records the key. If a
file is not supplied, `OPENAI_API_KEY` may be used directly. Keep both the key
file and the artifact directory outside the commit.

The runner obtains the exact Stable `mac-arm64` lock in
`scripts/webmcp-o0/chrome-for-testing.json` (`152.0.7977.64`, revision
`1669021`), verifies the official manifest, archive checksum, and executable
version, and launches a temporary profile with WebMCP enabled. Browser and
target IDs are derived by the same opaque ID mappers used by production
discovery; the spoken request does not contain either ID, a page-tool ref, or
encoded arguments.

## Evidence

The artifact directory contains the raw provider capture (`provider.json`),
semantic recording directory (`recording/`), request and assistant audio, and
`acceptance-report.json`. The report records the randomized request/value,
ordered provider calls and call IDs, raw `input_json`, complete textual
`webmcp.tool-result.v1` envelopes, Chrome pins, before/after HTTP oracle,
independent DOM oracle, target liveness, and the grounded transcript.

The validator requires this order:

```text
webmcp_list_tabs -> webmcp_select_tab -> webmcp_list_tools -> webmcp_invoke
```

It checks that `webmcp_invoke.tool_ref` reuses the returned catalog ref and
that `input_json` is one syntactically valid JSON object string containing only
the randomized fixture message. Invalid input is a failed Gate I2 measurement;
the runner does not rewrite arguments, retry the invocation, or weaken schema
validation. The failure text points to raw-schema acceleration as the next
controlled fallback.

Before putting evidence in a PR, sanitize paths, endpoint details, and any
credential-bearing material. Paste the sanitized report verbatim in the PR
body together with the exact sanitized command; never paste the API key, raw
WebSocket endpoint, or unredacted environment.
