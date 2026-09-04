# Built-in WebMCP site adapters

## Overview

`yui session --browser-tools webmcp` includes five default-on site adapters.
They are bundled into the executable and need no Chrome extension,
Tampermonkey script, or per-site feature flag.

| Adapter | Exact supported targets | Main tasks | Live-site caveats |
| --- | --- | --- | --- |
| YouTube | `youtube.com`, `www.youtube.com`, `m.youtube.com`; `youtu.be` redirects | Search and list ordinary videos; play; inspect state; pause/resume; seek; volume; captions | Age, consent, region, or sign-in restrictions can still block individual videos. |
| Spotify | `open.spotify.com` | Search/list tracks; play a returned track; inspect state; pause/resume; volume | A signed-in Web Player uses full playback. Signed-out sessions fall back to Spotify's public track preview; the embed has no volume control. |
| Wikipedia | `wikipedia.org` and language subdomains ending in `.wikipedia.org` | Full-text search; list/open a returned article; read bounded intro/headings | Only ordinary `/wiki/` articles are selectable; namespace links are excluded. |
| Reddit | `reddit.com`, `www.reddit.com`, `old.reddit.com` | Search/list non-promoted posts; open a returned post; read bounded title/body | Reddit may show a humanity or login challenge depending on network reputation. |
| Google Maps | `www.google.com/maps/*`, `maps.google.com/*` | Search/read a place; request/read directions | Current-location routes use Maps' browser location. Browser/OS permission or an unavailable position is reported explicitly. |

Tool names are prefixed with `youtube_`, `spotify_`, `wikipedia_`, `reddit_`,
or `google_maps_`. This keeps a mixed-tab model session unambiguous.

## How installation works

The implementation is an in-process adapter bundle, not a Chrome `.crx`
extension:

1. When the WebMCP harness attaches to a Chrome target, it calls
   `Page.addScriptToEvaluateOnNewDocument` with the bundled dispatcher and also
   evaluates it immediately in the current main world.
2. The dispatcher contains one isolated IIFE per adapter. Each IIFE begins
   with its own exact HTTPS hostname/path check and returns before defining
   anything on a non-matching page.
3. A matching adapter waits briefly for native `document.modelContext` or
   `navigator.modelContext`, then registers its tools with
   `modelContext.registerTool`.
4. Chrome's WebMCP domain reports the catalog to the existing target session.
   Broker policy, approvals, tab selection, generation-bound references,
   cancellation, recording, and redaction remain unchanged.
5. Every full navigation retires the old generation and its tool references.
   The adapter is evaluated again at document start, so agent-created tabs and
   redirects do not race the initial URL lookup.

Installing the dispatcher on every attached target is safe because every IIFE
returns before registration unless its in-page HTTPS host/path guard matches.
The Go registry independently mirrors those boundaries for metadata and
trusted-activation decisions. Unsupported targets register zero adapter tools.

Media actions that are subject to autoplay policy are declared separately in
the registry. Only YouTube play/resume and Spotify play/resume receive the
target session's narrow trusted-activation scope.

## Tool-design rules

Adapters intentionally expose workflow-level actions instead of arbitrary DOM
selectors or JavaScript execution:

- Return at most ten search results and bound all strings.
- Exclude ads/promoted results before returning structured data.
- Give each result a validated site-native identifier and a search-generation
  number.
- Allow open/play only for identifiers from the latest structured result set.
- Encode user text as URL/query data; never interpret it as HTML, a selector,
  script, or cross-origin URL.
- Return observed state after mutations. Full-player playback verifies
  media-time advancement. Spotify's signed-out preview verifies both an active
  play state and a fetched `mp3-preview` stream.
- Return typed failures such as `invalid_input`, `stale_result`,
  `signin_required`, `consent_required`, `location_permission_required`, or
  `site_changed` instead of attempting broad UI fallbacks.
- Split navigation from post-navigation observation. The tool initiating a
  full navigation returns `navigation_started`; the model refreshes the page
  catalog and calls the corresponding read/list tool in the new generation.

These constraints reduce model thrashing and make recordings suitable for
acceptance evidence.

## Adding a customer adapter

The current extension point is compile-time because loading arbitrary customer
JavaScript at runtime would expand browser authority and complicate artifact
review. Adding a first-party or customer-maintained adapter is otherwise small:

1. Add a standalone script at
   `agent-cli/internal/webmcp/siteadapter/extensions/<name>/<name>.js`.
2. Start it with `"use strict"`, an exact HTTPS host/path guard, and a unique
   non-configurable installation key. Keep all other state inside the IIFE.
3. Register a small, prefixed tool set through `modelContext.registerTool`.
   Use JSON Schemas with `additionalProperties: false` and tight length/range
   limits.
4. Add one `go:embed` variable and one `definition` entry in
   `siteadapter.go`. The entry supplies customer-visible metadata, the parsed
   URL matcher, and only the media actions that need trusted activation.
5. Extend `siteadapter_test.go` with positive URLs, deceptive lookalikes,
   HTTP/user-info cases, registry metadata, and stable tool-name checks.
6. Add a stock-Chrome fixture journey. It must exercise the production script,
   structured selection, state verification, promoted/ad exclusion where
   relevant, and at least one fabricated/stale identifier.
7. Run a recorded Realtime journey against the real site. Preserve the
   provider capture and semantic browser events, and scan artifacts for literal
   credentials before release.

The central registry is deliberately data-oriented; the Chrome target session
does not need a site-specific branch when another adapter is added.

## Verification

Run the ordinary regressions:

```bash
go test ./agent-cli/internal/webmcp/siteadapter \
  ./agent-cli/internal/webmcp/chrome \
  ./agent-cli/internal/webmcp \
  ./agent-cli/internal/webmcp/tools
```

Run the credential-free stock-Chrome site journeys:

```bash
WEBMCP_SITE_ADAPTER_INTEGRATION=1 \
  go test ./agent-cli/internal/webmcp/chrome \
  -run '^TestBundledSiteAdaptersStockChromeJourneys$' -count=1 -v

WEBMCP_YOUTUBE_ADAPTER_INTEGRATION=1 \
  go test ./agent-cli/internal/webmcp/chrome \
  -run '^TestYouTubeAdapterStockChromeJourney$' -count=1 -v
```

Before merging, run the repository-wide suite and one Realtime journey per
adapter. Live acceptance is successful only when the recording contains the
expected tool calls and grounded results. A typed authentication, consent,
CAPTCHA, or location-permission error is a valid diagnosis but not a successful
end-to-end acceptance run for the blocked operation.
