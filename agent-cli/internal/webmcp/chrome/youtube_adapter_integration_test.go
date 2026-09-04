package chrome

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/siteadapter"
)

const youtubeAdapterIntegrationEnv = "WEBMCP_YOUTUBE_ADAPTER_INTEGRATION"

// TestYouTubeAdapterStockChromeJourney is the credential-free, real-browser
// activation gate. It injects the production adapter through the same target
// session used in production, with only its origin guard replaced for the
// loopback fixture, then proves search, selection, audible play, and advancing
// media time through the generated WebMCP domain.
func TestYouTubeAdapterStockChromeJourney(t *testing.T) {
	if os.Getenv(youtubeAdapterIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the real-Chrome YouTube adapter journey", youtubeAdapterIntegrationEnv)
	}

	chromeExecutable, chromeVersion := findQualifiedStockChromeForIntegration(t)
	tone := youtubeAdapterToneWAV(4*time.Second, 24000, 440)
	fixture := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/tone.wav" {
			writer.Header().Set("Content-Type", "audio/wav")
			_, _ = writer.Write(tone)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Permissions-Policy", "tools=(self)")
		_, _ = fmt.Fprint(writer, youtubeAdapterFixtureHTML)
	}))
	t.Cleanup(fixture.Close)

	configDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	launcher := NewManagedBrowserLauncher(ManagedBrowserLaunchOptions{
		ConfigDir:  configDir,
		StartupURL: fixture.URL + "/",
		Headless:   true,
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

	target, err := waitForFixturePageTarget(ctx, browserHTTPURL(browser.Endpoint().BrowserWSEndpoint), fixture.URL+"/")
	if err != nil {
		t.Fatalf("discover fixture target: %v", err)
	}
	runtimeAdapter := NewRuntime()
	handle, err := runtimeAdapter.Open(ctx, webmcp.BrowserCandidate{
		ID: "youtube-adapter-browser", HTTPURL: browser.Endpoint().CDPURL,
		BrowserWSURL: browser.Endpoint().BrowserWSEndpoint, Loopback: true,
	})
	if err != nil {
		t.Fatalf("open browser runtime: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	session, err := handle.Attach(ctx, webmcp.TargetID(target.ID), webmcp.TargetOwnershipHarnessOwned)
	if err != nil {
		t.Fatalf("attach fixture target: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	targetSession, ok := session.(*targetSession)
	if !ok {
		t.Fatalf("session type = %T", session)
	}
	testSource := strings.Replace(siteadapter.YouTubeSource(), `if (location.protocol !== ALLOWED_PROTOCOL || !ALLOWED_HOSTS.has(location.hostname)) return;`, `if (location.hostname !== "127.0.0.1") return;`, 1)
	if testSource == siteadapter.YouTubeSource() {
		t.Fatal("test-only loopback origin substitution did not match the production script")
	}
	if err := targetSession.installPageScript(ctx, testSource); err != nil {
		t.Fatalf("inject adapter through target session: %v", err)
	}
	// Preserve the production origin decision while the actual document stays
	// on the loopback fixture used by this hermetic test.
	targetSession.mu.Lock()
	targetSession.page.URL = "https://www.youtube.com/"
	targetSession.mu.Unlock()
	if err := session.EnableWebMCP(ctx); err != nil {
		t.Fatalf("enable WebMCP: %v", err)
	}
	added, err := waitForIntegrationEvent(ctx, session.Events(), "YouTube adapter catalog", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolsAdded && hasTool(event.Tools, "youtube_search") && hasTool(event.Tools, "youtube_play_video")
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]webmcp.ToolDescriptor)
	for _, tool := range added.Tools {
		tools[tool.Name] = tool
	}
	if len(tools) != 10 {
		t.Fatalf("adapter catalog has %d tools, want 10: %v", len(tools), tools)
	}

	search := invokeYouTubeAdapterTool(t, ctx, session, tools["youtube_search"], `{"query":"test tone"}`)
	var searchResult struct {
		OK   bool `json:"ok"`
		Data struct {
			SearchGeneration int `json:"search_generation"`
			Results          []struct {
				VideoID string `json:"video_id"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal(search.Output, &searchResult); err != nil || !searchResult.OK || searchResult.Data.SearchGeneration != 1 || len(searchResult.Data.Results) != 1 || searchResult.Data.Results[0].VideoID != "tone1234567" {
		t.Fatalf("search response = %s, decode=%v", search.Output, err)
	}
	play := invokeYouTubeAdapterTool(t, ctx, session, tools["youtube_play_video"], `{"video_id":"tone1234567","search_generation":1}`)
	var playResult struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(play.Output, &playResult); err != nil || !playResult.OK {
		t.Fatalf("play response = %s, decode=%v; this is the media user-activation gate (error=%s)", play.Output, err, playResult.Error.Code)
	}

	first := inspectYouTubeAdapterPlayer(t, ctx, targetSession)
	time.Sleep(1200 * time.Millisecond)
	second := inspectYouTubeAdapterPlayer(t, ctx, targetSession)
	if first.Path != "/watch" || first.VideoID != "tone1234567" || first.Paused || first.ReadyState < 2 || second.CurrentTime <= first.CurrentTime || second.Muted || second.Volume <= 0 {
		t.Fatalf("independent player oracle first=%+v second=%+v", first, second)
	}
	t.Logf("WEBMCP_YOUTUBE_ADAPTER_PASS chrome=%s video=%s advance=%.3fs audible=true", chromeVersion, second.VideoID, second.CurrentTime-first.CurrentTime)
}

func invokeYouTubeAdapterTool(t *testing.T, ctx context.Context, session webmcp.TargetSession, tool webmcp.ToolDescriptor, input string) webmcp.BrowserEvent {
	t.Helper()
	if tool.Name == "" || tool.FrameID == "" {
		t.Fatalf("missing adapter tool descriptor: %+v", tool)
	}
	id, err := session.InvokeWebMCP(ctx, tool.FrameID, tool.Name, json.RawMessage(input))
	if err != nil {
		t.Fatalf("invoke %s: %v", tool.Name, err)
	}
	event, err := waitForIntegrationEvent(ctx, session.Events(), tool.Name+" terminal", func(event webmcp.BrowserEvent) bool {
		return event.Type == webmcp.EventToolResponded && event.InvocationID == id
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Status != "Completed" {
		t.Fatalf("%s terminal = %+v", tool.Name, event)
	}
	return event
}

type youtubeAdapterPlayerOracle struct {
	Path        string  `json:"path"`
	VideoID     string  `json:"video_id"`
	Paused      bool    `json:"paused"`
	Muted       bool    `json:"muted"`
	Volume      float64 `json:"volume"`
	ReadyState  int     `json:"ready_state"`
	CurrentTime float64 `json:"current_time"`
}

func inspectYouTubeAdapterPlayer(t *testing.T, ctx context.Context, session *targetSession) youtubeAdapterPlayerOracle {
	t.Helper()
	var oracle youtubeAdapterPlayerOracle
	expression := `(() => { const video = document.querySelector("video"); return { path: location.pathname, video_id: new URL(location.href).searchParams.get("v") || "", paused: video ? video.paused : true, muted: video ? video.muted : true, volume: video ? video.volume : 0, ready_state: video ? video.readyState : 0, current_time: video ? video.currentTime : 0 }; })()`
	if err := session.run(ctx, chromedp.Evaluate(expression, &oracle)); err != nil {
		t.Fatalf("inspect player: %v", err)
	}
	return oracle
}

func youtubeAdapterToneWAV(duration time.Duration, sampleRate int, frequency float64) []byte {
	samples := int(duration.Seconds() * float64(sampleRate))
	dataSize := samples * 2
	result := make([]byte, 44+dataSize)
	copy(result[0:4], "RIFF")
	binary.LittleEndian.PutUint32(result[4:8], uint32(36+dataSize))
	copy(result[8:12], "WAVE")
	copy(result[12:16], "fmt ")
	binary.LittleEndian.PutUint32(result[16:20], 16)
	binary.LittleEndian.PutUint16(result[20:22], 1)
	binary.LittleEndian.PutUint16(result[22:24], 1)
	binary.LittleEndian.PutUint32(result[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(result[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(result[32:34], 2)
	binary.LittleEndian.PutUint16(result[34:36], 16)
	copy(result[36:40], "data")
	binary.LittleEndian.PutUint32(result[40:44], uint32(dataSize))
	for index := 0; index < samples; index++ {
		value := int16(math.Sin(2*math.Pi*frequency*float64(index)/float64(sampleRate)) * 6000)
		binary.LittleEndian.PutUint16(result[44+index*2:46+index*2], uint16(value))
	}
	return result
}

const youtubeAdapterFixtureHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>YouTube adapter fixture</title>
<style>input,button,a,video{display:block;width:320px;height:40px;margin:10px}video{height:180px}</style></head>
<body><input id="search" name="search_query"><button id="search-icon-legacy">Search</button><main id="content"></main>
<script>
document.addEventListener("click", (event) => {
  const button = event.target.closest("button#search-icon-legacy");
  if (button) {
    event.preventDefault();
    history.pushState({}, "", "/results?search_query=" + encodeURIComponent(document.querySelector("#search").value));
    document.querySelector("#content").innerHTML = '<ytd-video-renderer><a id="video-title" title="Fixture tone" href="/watch?v=tone1234567">Fixture tone</a><div id="channel-name"><a>Fixture channel</a></div><ytd-thumbnail-overlay-time-status-renderer><span id="text">0:04</span></ytd-thumbnail-overlay-time-status-renderer></ytd-video-renderer>';
    return;
  }
  const anchor = event.target.closest("a[href*='/watch?v=']");
  if (anchor) {
    event.preventDefault();
    history.pushState({}, "", anchor.getAttribute("href"));
    document.body.innerHTML = '<h1 class="title">Fixture tone</h1><video src="/tone.wav"></video><button class="ytp-subtitles-button" aria-pressed="false" aria-disabled="false">CC</button>';
  }
});
</script></body></html>`
