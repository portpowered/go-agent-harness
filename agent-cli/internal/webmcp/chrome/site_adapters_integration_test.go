package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/siteadapter"
)

const siteAdaptersIntegrationEnv = "WEBMCP_SITE_ADAPTER_INTEGRATION"
const capitalOneShoppingAdapterIntegrationEnv = "WEBMCP_CAPITAL_ONE_SHOPPING_ADAPTER_INTEGRATION"
const xAdapterIntegrationEnv = "WEBMCP_X_ADAPTER_INTEGRATION"

func TestBundledSiteAdaptersStockChromeJourneys(t *testing.T) {
	if os.Getenv(siteAdaptersIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the stock-Chrome site-adapter journeys", siteAdaptersIntegrationEnv)
	}
	t.Run("spotify", testSpotifyAdapterJourney)
	t.Run("wikipedia", testWikipediaAdapterJourney)
	t.Run("reddit", testRedditAdapterJourney)
	t.Run("google_maps", testGoogleMapsAdapterJourney)
	t.Run("capital_one_shopping", testCapitalOneShoppingAdapterJourney)
	t.Run("x", testXAdapterJourney)
}

func TestCapitalOneShoppingAdapterStockChromeJourney(t *testing.T) {
	if os.Getenv(capitalOneShoppingAdapterIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the stock-Chrome Capital One Shopping adapter journey", capitalOneShoppingAdapterIntegrationEnv)
	}
	testCapitalOneShoppingAdapterJourney(t)
}

func TestXAdapterStockChromeJourney(t *testing.T) {
	if os.Getenv(xAdapterIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the stock-Chrome X adapter journey", xAdapterIntegrationEnv)
	}
	testXAdapterJourney(t)
}

type adapterFixture struct {
	ctx     context.Context
	session webmcp.TargetSession
	target  *targetSession
	tools   map[string]webmcp.ToolDescriptor
	count   int
	version string
}

func newAdapterFixture(t *testing.T, name, supportedURL, source, guard string, handler http.HandlerFunc) adapterFixture {
	t.Helper()
	chromeExecutable, chromeVersion := findQualifiedStockChromeForIntegration(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	t.Cleanup(cancel)
	launcher := NewManagedBrowserLauncher(ManagedBrowserLaunchOptions{
		ConfigDir: t.TempDir(), StartupURL: server.URL + "/", Headless: true,
		Acquirer: ManagedChromeExecutableAcquirerFunc(func(context.Context) (ChromeExecutable, error) {
			return ChromeExecutable{Path: chromeExecutable, Version: chromeVersion, Major: MinimumManagedChromeMajor, Source: ExecutableSourceStock}, nil
		}),
		StartupTimeout: 20 * time.Second,
	})
	browser, err := launcher.Launch(ctx)
	if err != nil {
		t.Fatalf("launch stock Chrome: %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })
	target, err := waitForFixturePageTarget(ctx, browserHTTPURL(browser.Endpoint().BrowserWSEndpoint), server.URL+"/")
	if err != nil {
		t.Fatalf("discover fixture target: %v", err)
	}
	runtimeAdapter := NewRuntime()
	handle, err := runtimeAdapter.Open(ctx, webmcp.BrowserCandidate{ID: webmcp.BrowserID(name + "-adapter-browser"), HTTPURL: browser.Endpoint().CDPURL, BrowserWSURL: browser.Endpoint().BrowserWSEndpoint, Loopback: true})
	if err != nil {
		t.Fatalf("open browser runtime: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	session, err := handle.Attach(ctx, webmcp.TargetID(target.ID), webmcp.TargetOwnershipHarnessOwned)
	if err != nil {
		t.Fatalf("attach fixture target: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	targetSession := session.(*targetSession)
	testSource := strings.Replace(source, guard, `if (location.hostname !== "127.0.0.1") return;`, 1)
	if testSource == source {
		t.Fatalf("test-only loopback origin substitution did not match %s source", name)
	}
	testSource = strings.ReplaceAll(testSource, "https://www.google.com/maps", "${location.origin}/maps")
	if err := targetSession.installPageScript(ctx, testSource); err != nil {
		t.Fatalf("inject %s adapter: %v", name, err)
	}
	targetSession.mu.Lock()
	targetSession.page.URL = supportedURL
	targetSession.mu.Unlock()
	if err := session.EnableWebMCP(ctx); err != nil {
		t.Fatalf("enable WebMCP: %v", err)
	}
	expected := map[string]int{"spotify": 8, "wikipedia": 5, "reddit": 5, "google-maps": 5, "capital-one-shopping": 4, "x": 4}[name]
	tools := waitForAdapterCatalog(t, ctx, session, "adapter catalog", expected)
	return adapterFixture{ctx: ctx, session: session, target: targetSession, tools: tools, count: expected, version: chromeVersion}
}

func waitForAdapterCatalog(t *testing.T, ctx context.Context, session webmcp.TargetSession, label string, expected int) map[string]webmcp.ToolDescriptor {
	t.Helper()
	tools := make(map[string]webmcp.ToolDescriptor, expected)
	for len(tools) < expected {
		added, err := waitForIntegrationEvent(ctx, session.Events(), label, func(event webmcp.BrowserEvent) bool {
			return event.Type == webmcp.EventToolsAdded && len(event.Tools) > 0
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range added.Tools {
			tools[tool.Name] = tool
		}
	}
	return tools
}

func invokeAdapterTool(t *testing.T, fixture adapterFixture, name, input string) json.RawMessage {
	t.Helper()
	event := invokeAdapterToolEvent(t, fixture, name, input)
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(event.Output, &envelope); err != nil || !envelope.OK {
		t.Fatalf("%s output = %s decode=%v code=%s", name, event.Output, err, envelope.Error.Code)
	}
	return event.Output
}

func invokeAdapterToolEvent(t *testing.T, fixture adapterFixture, name, input string) webmcp.BrowserEvent {
	t.Helper()
	tool := fixture.tools[name]
	if tool.Name == "" {
		available := make([]string, 0, len(fixture.tools))
		for candidate := range fixture.tools {
			available = append(available, candidate)
		}
		t.Fatalf("adapter tool %q missing; available=%v", name, available)
	}
	return invokeYouTubeAdapterTool(t, fixture.ctx, fixture.session, tool, input)
}

func requireAdapterFailure(t *testing.T, fixture adapterFixture, name, input, code string) {
	t.Helper()
	event := invokeAdapterToolEvent(t, fixture, name, input)
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(event.Output, &envelope); err != nil || envelope.OK || envelope.Error.Code != code {
		t.Fatalf("%s adversarial output = %s decode=%v; want code=%s", name, event.Output, err, code)
	}
}

func refreshAdapterAfterNavigation(t *testing.T, fixture *adapterFixture, supportedURL string) {
	t.Helper()
	fixture.tools = waitForAdapterCatalog(t, fixture.ctx, fixture.session, "post-navigation adapter catalog", fixture.count)
	fixture.target.mu.Lock()
	fixture.target.page.URL = supportedURL
	fixture.target.mu.Unlock()
}

func adapterFixtureHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Permissions-Policy", "tools=(self)")
}

func testWikipediaAdapterJourney(t *testing.T) {
	source, _ := siteadapter.Source(siteadapter.WikipediaName)
	handler := func(writer http.ResponseWriter, request *http.Request) {
		adapterFixtureHeaders(writer)
		body := `<h1>Wikipedia</h1>`
		if request.URL.Path == "/w/index.php" {
			body = `<div class="mw-search-result"><div class="mw-search-result-heading"><a href="/wiki/Go_(programming_language)">Go (programming language)</a></div><div class="searchresult">A programming language.</div></div>`
		} else if request.URL.Path == "/wiki/Go_(programming_language)" {
			body = `<h1 id="firstHeading">Go (programming language)</h1><div class="mw-parser-output"><p>Go is a statically typed programming language designed at Google.</p><h2>History</h2></div>`
		}
		_, _ = fmt.Fprint(writer, `<!doctype html><body>`+body+`</body>`)
	}
	fixture := newAdapterFixture(t, "wikipedia", "https://en.wikipedia.org/", source, `if (location.protocol !== "https:" || !(host === "wikipedia.org" || host.endsWith(".wikipedia.org"))) return;`, handler)
	invokeAdapterTool(t, fixture, "wikipedia_search", `{"query":"Go programming language"}`)
	refreshAdapterAfterNavigation(t, &fixture, "https://en.wikipedia.org/w/index.php")
	output := invokeAdapterTool(t, fixture, "wikipedia_list_results", `{}`)
	var listed struct {
		Data struct {
			Generation int `json:"search_generation"`
			Results    []struct {
				Key string `json:"page_key"`
			} `json:"results"`
		} `json:"data"`
	}
	_ = json.Unmarshal(output, &listed)
	if listed.Data.Generation != 1 || len(listed.Data.Results) != 1 {
		t.Fatalf("Wikipedia results = %s", output)
	}
	requireAdapterFailure(t, fixture, "wikipedia_open_result", `{"page_key":"/wiki/Injected","search_generation":1}`, "stale_result")
	invokeAdapterTool(t, fixture, "wikipedia_open_result", `{"page_key":"/wiki/Go_(programming_language)","search_generation":1}`)
	refreshAdapterAfterNavigation(t, &fixture, "https://en.wikipedia.org/wiki/Go_(programming_language)")
	article := invokeAdapterTool(t, fixture, "wikipedia_read_article", `{}`)
	if !strings.Contains(string(article), "statically typed") {
		t.Fatalf("Wikipedia article = %s", article)
	}
	t.Logf("WEBMCP_WIKIPEDIA_ADAPTER_PASS chrome=%s", fixture.version)
}

func testRedditAdapterJourney(t *testing.T) {
	source, _ := siteadapter.Source(siteadapter.RedditName)
	source = strings.Replace(source, `new Set(["reddit.com", "www.reddit.com", "old.reddit.com"])`, `new Set(["reddit.com", "www.reddit.com", "old.reddit.com", "127.0.0.1"])`, 1)
	handler := func(writer http.ResponseWriter, request *http.Request) {
		adapterFixtureHeaders(writer)
		body := `<h1>Reddit</h1>`
		if request.URL.Path == "/search/" {
			body = `<article promoted><h2>Promoted</h2><a href="/comments/ad123/promoted/">Ad</a></article><article><h2>Go concurrency patterns</h2><a href="/r/golang/comments/post123/go_concurrency/">Go concurrency patterns</a></article>`
		} else if strings.Contains(request.URL.Path, "/comments/post123/") {
			body = `<main><article><h1>Go concurrency patterns</h1><div data-post-click-location="text-body">Useful discussion of goroutines and channels.</div></article></main>`
		}
		_, _ = fmt.Fprint(writer, `<!doctype html><body>`+body+`</body>`)
	}
	fixture := newAdapterFixture(t, "reddit", "https://www.reddit.com/", source, `if (location.protocol !== "https:" || !ALLOWED_HOSTS.has(location.hostname.toLowerCase())) return;`, handler)
	invokeAdapterTool(t, fixture, "reddit_search", `{"query":"Go concurrency"}`)
	refreshAdapterAfterNavigation(t, &fixture, "https://www.reddit.com/search/")
	output := invokeAdapterTool(t, fixture, "reddit_list_posts", `{}`)
	if strings.Contains(string(output), "ad123") || !strings.Contains(string(output), "post123") {
		t.Fatalf("Reddit results did not exclude promoted content: %s", output)
	}
	requireAdapterFailure(t, fixture, "reddit_open_post", `{"post_id":"ad123","search_generation":1}`, "stale_result")
	invokeAdapterTool(t, fixture, "reddit_open_post", `{"post_id":"post123","search_generation":1}`)
	refreshAdapterAfterNavigation(t, &fixture, "https://www.reddit.com/r/golang/comments/post123/go_concurrency/")
	post := invokeAdapterTool(t, fixture, "reddit_read_post", `{}`)
	if !strings.Contains(string(post), "goroutines and channels") {
		t.Fatalf("Reddit post = %s", post)
	}
	t.Logf("WEBMCP_REDDIT_ADAPTER_PASS chrome=%s", fixture.version)
}

func testSpotifyAdapterJourney(t *testing.T) {
	source, _ := siteadapter.Source(siteadapter.SpotifyName)
	tone := youtubeAdapterToneWAV(8*time.Second, 24000, 440)
	handler := func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/tone.wav" {
			writer.Header().Set("Content-Type", "audio/wav")
			_, _ = writer.Write(tone)
			return
		}
		adapterFixtureHeaders(writer)
		body := `<h1>Spotify</h1>`
		if strings.HasPrefix(request.URL.Path, "/search/") {
			body = `<div data-testid="tracklist-row"><a href="/track/track123456789">Fixture Song</a><button data-testid="play-button" aria-label="Play Fixture Song">Play</button></div><div data-testid="volume-bar"><input type="range" min="0" max="1" step="0.01" value="1"></div>`
		} else if strings.HasPrefix(request.URL.Path, "/embed/track/") {
			body = `<h1 data-testid="track-name">Fixture Song</h1><audio src="/tone.wav"></audio><button data-testid="play-pause-button" aria-label="Play">Play</button><script>const audio=document.querySelector("audio"),control=document.querySelector("[data-testid=play-pause-button]");const sync=()=>control.setAttribute("aria-label",audio.paused?"Play":"Pause");control.onclick=async()=>{audio.paused?await audio.play():audio.pause();sync()}</script>`
		}
		_, _ = fmt.Fprint(writer, `<!doctype html><body>`+body+`</body>`)
	}
	fixture := newAdapterFixture(t, "spotify", "https://open.spotify.com/", source, `if (location.protocol !== "https:" || location.hostname.toLowerCase() !== "open.spotify.com") return;`, handler)
	invokeAdapterTool(t, fixture, "spotify_search_tracks", `{"query":"Fixture Song"}`)
	refreshAdapterAfterNavigation(t, &fixture, "https://open.spotify.com/search/Fixture%20Song/tracks")
	tracks := invokeAdapterTool(t, fixture, "spotify_list_tracks", `{}`)
	if !strings.Contains(string(tracks), "track123456789") {
		t.Fatalf("Spotify tracks = %s", tracks)
	}
	requireAdapterFailure(t, fixture, "spotify_play_track", `{"track_id":"attacker123456","search_generation":1}`, "stale_result")
	invokeAdapterTool(t, fixture, "spotify_set_volume", `{"volume":37}`)
	played := invokeAdapterTool(t, fixture, "spotify_play_track", `{"track_id":"track123456789","search_generation":1}`)
	if !strings.Contains(string(played), "navigation_started") || !strings.Contains(string(played), `"preview":true`) {
		t.Fatalf("Spotify play = %s", played)
	}
	refreshAdapterAfterNavigation(t, &fixture, "https://open.spotify.com/embed/track/track123456789")
	resumed := invokeAdapterTool(t, fixture, "spotify_resume", `{}`)
	if !strings.Contains(string(resumed), "verified_advance_seconds") {
		t.Fatalf("Spotify preview resume = %s", resumed)
	}
	invokeAdapterTool(t, fixture, "spotify_pause", `{}`)
	invokeAdapterTool(t, fixture, "spotify_resume", `{}`)
	t.Logf("WEBMCP_SPOTIFY_ADAPTER_PASS chrome=%s audible=true signed_out_preview=true", fixture.version)
}

func testGoogleMapsAdapterJourney(t *testing.T) {
	source, _ := siteadapter.Source(siteadapter.GoogleMapsName)
	handler := func(writer http.ResponseWriter, request *http.Request) {
		adapterFixtureHeaders(writer)
		body := `<h1>Maps</h1>`
		if strings.HasPrefix(request.URL.Path, "/maps/search/") {
			body = `<main><h1 class="DUwDvf">Golden Gate Bridge</h1><button data-item-id="address">Golden Gate Bridge, San Francisco, CA</button><div class="F7nice">4.8 stars</div></main>`
		} else if strings.HasPrefix(request.URL.Path, "/maps/dir/") {
			body = `<main><div id="directions-searchbox-0"><input aria-label="Starting point" value="Current location"></div><div id="directions-searchbox-1"><input aria-label="Destination" value="Golden Gate Bridge"></div><div data-trip-index="0">Fastest route 20 min via US-101</div></main>`
		}
		_, _ = fmt.Fprint(writer, `<!doctype html><body>`+body+`</body>`)
	}
	fixture := newAdapterFixture(t, "google-maps", "https://www.google.com/maps/", source, `if (location.protocol !== "https:" || !((host === "www.google.com" && (location.pathname === "/maps" || location.pathname.startsWith("/maps/"))) || host === "maps.google.com")) return;`, handler)
	invokeAdapterTool(t, fixture, "google_maps_search_place", `{"query":"Golden Gate Bridge"}`)
	refreshAdapterAfterNavigation(t, &fixture, "https://www.google.com/maps/search/")
	place := invokeAdapterTool(t, fixture, "google_maps_read_place", `{}`)
	if !strings.Contains(string(place), "Golden Gate Bridge") {
		t.Fatalf("Maps place = %s", place)
	}
	invokeAdapterTool(t, fixture, "google_maps_directions", `{"destination":"Golden Gate Bridge","travel_mode":"driving"}`)
	refreshAdapterAfterNavigation(t, &fixture, "https://www.google.com/maps/dir/")
	route := invokeAdapterTool(t, fixture, "google_maps_read_route", `{}`)
	if !strings.Contains(string(route), "20 min") || !strings.Contains(string(route), "Current location") {
		t.Fatalf("Maps route = %s", route)
	}
	t.Logf("WEBMCP_GOOGLE_MAPS_ADAPTER_PASS chrome=%s current_location=true", fixture.version)
}

func testCapitalOneShoppingAdapterJourney(t *testing.T) {
	source, _ := siteadapter.Source(siteadapter.CapitalOneShoppingName)
	// Keep the hermetic end-of-feed branch fast while production retains the
	// four-second allowance required by live lazy-loaded pages.
	source = strings.Replace(source, "Date.now() + 4000", "Date.now() + 250", 1)
	handler := func(writer http.ResponseWriter, _ *http.Request) {
		adapterFixtureHeaders(writer)
		_, _ = fmt.Fprint(writer, capitalOneShoppingAdapterFixtureHTML)
	}
	fixture := newAdapterFixture(t, "capital-one-shopping", "https://capitaloneshopping.com/", source, `if (location.protocol !== "https:" || !ALLOWED_HOSTS.has(location.hostname.toLowerCase())) return;`, handler)

	requireAdapterFailure(t, fixture, "capital_one_shopping_scan_offers", `{"max_pages":21}`, "invalid_input")
	output := invokeAdapterTool(t, fixture, "capital_one_shopping_scan_offers", `{"max_pages":6,"max_cost_usd":500,"min_cashback_percent":70,"min_bonus_usd":300,"reward_match":"any","unknown_cost_policy":"separate"}`)
	var scan struct {
		Data struct {
			PagesScanned          int    `json:"pages_scanned"`
			OffersObserved        int    `json:"offers_observed"`
			MatchCount            int    `json:"match_count"`
			UnknownCostMatchCount int    `json:"unknown_cost_match_count"`
			StopReason            string `json:"stop_reason"`
			Generation            int    `json:"scan_generation"`
			Matches               []struct {
				Merchant           string   `json:"merchant"`
				CashbackPercent    *float64 `json:"cashback_percent"`
				BonusUSD           *float64 `json:"bonus_usd"`
				RewardCapUSD       *float64 `json:"reward_cap_usd"`
				QualifyingSpendUSD *float64 `json:"qualifying_spend_usd"`
				CostUSD            *float64 `json:"cost_usd"`
			} `json:"matches"`
			Unknown []struct {
				Merchant string `json:"merchant"`
			} `json:"unknown_cost_matches"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &scan); err != nil {
		t.Fatalf("decode Capital One Shopping scan: %v: %s", err, output)
	}
	if scan.Data.PagesScanned < 3 || scan.Data.StopReason != "no_growth" || scan.Data.OffersObserved < 6 {
		t.Fatalf("Capital One Shopping scan lifecycle = %+v; output=%s", scan.Data, output)
	}
	if scan.Data.MatchCount != 3 || scan.Data.UnknownCostMatchCount != 1 || len(scan.Data.Matches) != 3 || len(scan.Data.Unknown) != 1 {
		t.Fatalf("Capital One Shopping match counts = %+v; output=%s", scan.Data, output)
	}
	if !strings.Contains(string(output), `"merchant":"Exact Seventy"`) ||
		!strings.Contains(string(output), `"merchant":"Exact Three Hundred"`) ||
		!strings.Contains(string(output), `"merchant":"Late Bonus"`) ||
		!strings.Contains(string(output), `"merchant":"Unknown Price"`) {
		t.Fatalf("Capital One Shopping expected merchants missing: %s", output)
	}
	if strings.Contains(string(output), `"merchant":"Reward Cap Trap"`) || strings.Contains(string(output), `"merchant":"Over Budget"`) {
		t.Fatalf("Capital One Shopping cap/cost filters admitted an invalid match: %s", output)
	}

	listed := invokeAdapterTool(t, fixture, "capital_one_shopping_list_matches", fmt.Sprintf(`{"scan_generation":%d,"offset":1,"limit":1}`, scan.Data.Generation))
	if !strings.Contains(string(listed), `"total":3`) || !strings.Contains(string(listed), `"limit":1`) {
		t.Fatalf("Capital One Shopping bounded list = %s", listed)
	}
	requireAdapterFailure(t, fixture, "capital_one_shopping_list_matches", `{"scan_generation":999}`, "stale_result")
	invokeAdapterTool(t, fixture, "capital_one_shopping_reset_scan", `{}`)
	requireAdapterFailure(t, fixture, "capital_one_shopping_list_matches", fmt.Sprintf(`{"scan_generation":%d}`, scan.Data.Generation), "stale_result")
	t.Logf("WEBMCP_CAPITAL_ONE_SHOPPING_ADAPTER_PASS chrome=%s pages=%d offers=%d", fixture.version, scan.Data.PagesScanned, scan.Data.OffersObserved)
}

func testXAdapterJourney(t *testing.T) {
	source, _ := siteadapter.Source(siteadapter.XName)
	handler := func(writer http.ResponseWriter, _ *http.Request) {
		adapterFixtureHeaders(writer)
		_, _ = fmt.Fprint(writer, xAdapterFixtureHTML)
	}
	fixture := newAdapterFixture(t, "x", "https://x.com/home", source, `if (location.protocol !== "https:" || !ALLOWED_HOSTS.has(location.hostname.toLowerCase())) return;`, handler)

	contextOutput := invokeAdapterTool(t, fixture, "x_get_context", `{}`)
	if !strings.Contains(string(contextOutput), `"signed_in":true`) || !strings.Contains(string(contextOutput), `"account_handle":"@fixture_user"`) {
		t.Fatalf("X context = %s", contextOutput)
	}
	preparedOutput := invokeAdapterTool(t, fixture, "x_prepare_post", `{"text":"this is a test of the webmcp connection"}`)
	var prepared struct {
		Data struct {
			Token string `json:"draft_token"`
			Text  string `json:"text"`
		} `json:"data"`
	}
	if err := json.Unmarshal(preparedOutput, &prepared); err != nil || prepared.Data.Token == "" || prepared.Data.Text != "this is a test of the webmcp connection" {
		t.Fatalf("decode X prepared draft: %v: %s", err, preparedOutput)
	}
	requireAdapterFailure(t, fixture, "x_publish_post", fmt.Sprintf(`{"draft_token":%q,"text":"changed","confirm":true}`, prepared.Data.Token), "text_mismatch")
	requireAdapterFailure(t, fixture, "x_publish_post", fmt.Sprintf(`{"draft_token":%q,"text":%q,"confirm":false}`, prepared.Data.Token, prepared.Data.Text), "confirmation_required")
	published := invokeAdapterTool(t, fixture, "x_publish_post", fmt.Sprintf(`{"draft_token":%q,"text":%q,"confirm":true}`, prepared.Data.Token, prepared.Data.Text))
	if !strings.Contains(string(published), `"published":true`) || !strings.Contains(string(published), `"duplicate_retry_blocked":true`) {
		t.Fatalf("X publish result = %s", published)
	}
	requireAdapterFailure(t, fixture, "x_publish_post", fmt.Sprintf(`{"draft_token":%q,"text":%q,"confirm":true}`, prepared.Data.Token, prepared.Data.Text), "already_published")

	second := invokeAdapterTool(t, fixture, "x_prepare_post", `{"text":"draft to clear"}`)
	if err := json.Unmarshal(second, &prepared); err != nil || prepared.Data.Token == "" {
		t.Fatalf("decode second X draft: %v: %s", err, second)
	}
	cleared := invokeAdapterTool(t, fixture, "x_clear_draft", fmt.Sprintf(`{"draft_token":%q}`, prepared.Data.Token))
	if !strings.Contains(string(cleared), `"cleared":true`) || !strings.Contains(string(cleared), `"published":false`) {
		t.Fatalf("X clear result = %s", cleared)
	}
	t.Logf("WEBMCP_X_ADAPTER_PASS chrome=%s one_use_publish=true", fixture.version)
}

const capitalOneShoppingAdapterFixtureHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Capital One Shopping adapter fixture</title>
<style>body{margin:0}#offers{min-height:1400px}.offer{display:block;margin:24px;height:180px;width:640px}.spacer{height:900px}</style></head>
<body><main><div id="offers"></div><div class="spacer"></div></main>
<script>
const batches = [
  [
    {merchant:"Exact Seventy", promo:"25% off Exact Seventy plans.", reward:"Now 70% back ($1,000 max)", price:"$450", detail:"Eligible product"},
    {merchant:"Exact Three Hundred", reward:"Get $300 back when you spend $1,000", price:"$499", detail:"Activation bonus"},
    {merchant:"Reward Cap Trap", reward:"Now 5% back ($1,000 max)", price:"$20", detail:"Low reward"}
  ],
  [
    {merchant:"Exact Seventy", promo:"25% off Exact Seventy plans.", reward:"Now 70% back ($1,000 max)", price:"$450", detail:"Eligible product"},
    {merchant:"Unknown Price", reward:"Now 80% back ($1,000 max)", price:"", detail:"Merchant offer"},
    {merchant:"Over Budget", reward:"Now 90% back ($1,000 max)", price:"$800", detail:"Expensive product"}
  ],
  [
    {merchant:"Late Bonus", reward:"Now up to $350 back", price:"$200", detail:"Late-loaded bonus"}
  ]
];
let batch = 0;
const offers = document.querySelector("#offers");
const render = (rows) => {
  for (const row of rows) {
    const button = document.createElement("button");
    button.className = "offer";
    button.setAttribute("aria-label", "View " + row.merchant + " offer - " + row.reward);
    button.innerHTML = '<img alt="Merchant image for ' + row.merchant + '"><p>' + row.merchant + '</p>' + (row.promo ? '<p>' + row.promo + '</p>' : '') + '<p>' + row.reward + '</p>' + (row.price ? '<span class="product-price">' + row.price + '</span>' : '') + '<p>' + row.detail + '</p><span>Get this Offer</span>';
    offers.appendChild(button);
  }
};
render(batches[0]);
addEventListener("scroll", () => {
  if (scrollY + innerHeight < document.documentElement.scrollHeight - 5 || batch >= batches.length - 1) return;
  batch += 1;
  if (batch === 2) offers.querySelectorAll("button")[0]?.remove();
  render(batches[batch]);
  document.querySelector(".spacer").style.height = (900 + batch * 700) + "px";
});
</script></body></html>`

const xAdapterFixtureHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>X adapter fixture</title></head>
<body>
<nav><a data-testid="AppTabBar_Profile_Link" aria-label="Profile" href="/fixture_user">Profile</a><a data-testid="SideNav_NewTweet_Button" href="/compose/post">Post</a></nav>
<main>
  <div data-testid="tweetTextarea_0" role="textbox" contenteditable="true"></div>
  <button data-testid="tweetButtonInline">Post</button>
  <div id="published"></div>
</main>
<script>
document.querySelector('[data-testid="tweetButtonInline"]').addEventListener("click", () => {
  const composer = document.querySelector('[data-testid="tweetTextarea_0"]');
  const post = document.createElement("article");
  post.textContent = composer.innerText || composer.textContent;
  document.querySelector("#published").appendChild(post);
  composer.textContent = "";
  composer.dispatchEvent(new InputEvent("input", {bubbles:true, inputType:"deleteContentBackward"}));
});
</script></body></html>`
