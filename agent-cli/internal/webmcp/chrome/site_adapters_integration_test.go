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

func TestBundledSiteAdaptersStockChromeJourneys(t *testing.T) {
	if os.Getenv(siteAdaptersIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the stock-Chrome site-adapter journeys", siteAdaptersIntegrationEnv)
	}
	t.Run("spotify", testSpotifyAdapterJourney)
	t.Run("wikipedia", testWikipediaAdapterJourney)
	t.Run("reddit", testRedditAdapterJourney)
	t.Run("google_maps", testGoogleMapsAdapterJourney)
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
	expected := map[string]int{"spotify": 8, "wikipedia": 5, "reddit": 5, "google-maps": 5}[name]
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
