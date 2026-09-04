# YouTube WebMCP site adapter

## Product behavior

With `--browser-tools webmcp`, selecting an HTTPS YouTube target automatically
adds ten structured page tools:

- `youtube_get_context`
- `youtube_search`
- `youtube_list_results`
- `youtube_play_video`
- `youtube_get_player_state`
- `youtube_pause` and `youtube_resume`
- `youtube_seek`
- `youtube_set_volume`
- `youtube_set_captions`

There is no YouTube feature flag, extension, Tampermonkey dependency, or
customer installation step. The normal browser capability remains explicit;
the adapter is default-on whenever that capability attaches to a supported
YouTube link.

## Architecture and safety boundaries

The Chrome target session installs the bundled site-adapter dispatcher with
CDP `Page.addScriptToEvaluateOnNewDocument` for reload/redirect continuity and
evaluates it immediately in the current main world. The YouTube IIFE returns
without registering anything unless the document is HTTPS on `youtube.com`,
`www.youtube.com`, or `m.youtube.com`. The Go registry also recognizes
redirecting `youtu.be` targets. Installing the dispatcher before a newly opened
tab's URL is final avoids an `about:blank` attachment race while preserving the
exact in-page origin boundary.

Search results are bounded to ten visible ordinary video links. Ads are
excluded, video IDs are validated, and play accepts only an ID from the current
structured result generation. All mutations use YouTube's visible controls or
HTML media element and return observed state. Play and resume receive a
trusted-activation scope immediately before the WebMCP invocation so first
playback works without an unrelated customer click.

## Verification

Run the deterministic package and real-browser gates:

```bash
go test ./agent-cli/internal/webmcp/siteadapter \
  ./agent-cli/internal/webmcp/chrome \
  ./agent-cli/internal/config \
  ./agent-cli/internal/cli

WEBMCP_YOUTUBE_ADAPTER_INTEGRATION=1 \
  go test ./agent-cli/internal/webmcp/chrome \
  -run '^TestYouTubeAdapterStockChromeJourney$' -count=1 -v
```

The integration gate launches qualified stock Chrome against a hermetic
YouTube-shaped fixture and proves native WebMCP registration, search, result
selection, audible playback, and advancing media time.

The release acceptance run uses the production `yui` binary, a real YouTube
page, and OpenAI Realtime:

```bash
go build -o /tmp/yui-youtube ./agent-cli/cmd/yui

AGENT_MODEL__OPENAI__API_KEY="$OPENAI_API_KEY" \
/tmp/yui-youtube session \
  --provider openai \
  --model gpt-realtime-2.1 \
  --browser-tools webmcp \
  --browser-open https://www.youtube.com/ \
  --browser-allowed-origin https://www.youtube.com \
  --browser-approval never \
  --browser-record \
  --browser-record-arguments \
  --browser-record-results \
  --record /tmp/youtube-provider.json \
  --record-dir /tmp/youtube-recording \
  --no-terminal-tools \
  --prompt 'Use the browser tools on YouTube. Search for NASA space documentary, choose one ordinary non-ad video from the current results, play it, then tell me the exact title and whether playback is advancing.' \
  --max-duration 90s
```

Release evidence must include the exact Chrome version, completed
`youtube_search` and `youtube_play_video` tool responses in the semantic
recording, the assistant's grounded final response, and an independent player
oracle showing `paused=false`, `readyState >= 2`, and increasing
`currentTime`. Never record or print the API key.
