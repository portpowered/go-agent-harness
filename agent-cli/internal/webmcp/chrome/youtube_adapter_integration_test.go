//go:build e2e

package chrome

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cdpRuntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/siteadapter"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// TestYouTubeAdapterStockChromeJourney is the credential-free, real-browser
// activation gate. It injects the production adapter through the same target
// session used in production, with only its origin guard replaced for the
// loopback fixture, then proves search, selection, audible play, and advancing
// media time through the generated WebMCP domain.
func TestYouTubeAdapterStockChromeJourney(t *testing.T) {
	chromeExecutable, chromeVersion := findQualifiedStockChromeForIntegration(t)
	tone := youtubeAdapterToneWAV(4*time.Second, 24000, 440)
	fixture := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/tone.wav" {
			writer.Header().Set("Content-Type", "audio/wav")
			writeFixtureBody(writer, tone)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Permissions-Policy", "tools=(self)")
		writeFixtureBody(writer, []byte(youtubeAdapterFixtureHTML))
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
	t.Cleanup(func() { discardSecondaryError(browser.Close) })

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
	t.Cleanup(func() { discardSecondaryError(handle.Close) })
	session, err := handle.Attach(ctx, webmcp.TargetID(target.ID), webmcp.TargetOwnershipHarnessOwned)
	if err != nil {
		t.Fatalf("attach fixture target: %v", err)
	}
	t.Cleanup(func() { discardSecondaryError(session.Close) })
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

	assertYouTubeAdapterMediaTools(t, ctx, session, tools)

	first := waitForYouTubeAdapterPlayer(t, ctx, targetSession, "tone1234567")
	waitForYouTubeAdapterPlaybackAdvance(t, ctx, targetSession, first.CurrentTime+youtubeAdapterPlaybackAdvanceSeconds)
	second := inspectYouTubeAdapterPlayer(t, ctx, targetSession)
	if first.Path != "/watch" || first.VideoID != "tone1234567" || first.Paused || first.ReadyState < 2 || second.CurrentTime <= first.CurrentTime || second.Muted || second.Volume <= 0 {
		t.Fatalf("independent player oracle first=%+v second=%+v", first, second)
	}
	t.Logf("WEBMCP_YOUTUBE_ADAPTER_PASS chrome=%s video=%s advance=%.3fs audible=true", chromeVersion, second.VideoID, second.CurrentTime-first.CurrentTime)
}

// assertYouTubeAdapterMediaTools drives search, selection, and audible play
// through the generated WebMCP domain and checks each adapter response.
func assertYouTubeAdapterMediaTools(t *testing.T, ctx context.Context, session webmcp.TargetSession, tools map[string]webmcp.ToolDescriptor) {
	t.Helper()
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

	playerState := invokeYouTubeAdapterTool(t, ctx, session, tools["youtube_get_player_state"], `{}`)
	var playerStateResult struct {
		OK   bool `json:"ok"`
		Data struct {
			VerifiedAdvanceSeconds float64 `json:"verified_advance_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(playerState.Output, &playerStateResult); err != nil || !playerStateResult.OK || playerStateResult.Data.VerifiedAdvanceSeconds <= 0 {
		t.Fatalf("player-state response = %s, decode=%v", playerState.Output, err)
	}
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
	if event.Status != toolStatusCompleted {
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

// youtubeAdapterPlaybackAdvanceSeconds is how much media time must elapse
// between the two independent player observations.
const youtubeAdapterPlaybackAdvanceSeconds = 1.0

// waitForYouTubeAdapterPlaybackAdvance resolves inside the page once the
// video's own timeupdate events report a current time of at least target
// seconds, so the oracle waits on the media clock instead of a fixed delay.
func waitForYouTubeAdapterPlaybackAdvance(t *testing.T, ctx context.Context, session *targetSession, target float64) {
	t.Helper()
	expression := fmt.Sprintf(`new Promise((resolve) => { const video = document.querySelector("video"); if (!video) { resolve(false); return; } const check = () => { if (video.currentTime >= %f) { video.removeEventListener("timeupdate", check); resolve(true); } }; video.addEventListener("timeupdate", check); check(); })`, target)
	var advanced bool
	await := func(params *cdpRuntime.EvaluateParams) *cdpRuntime.EvaluateParams {
		return params.WithAwaitPromise(true)
	}
	if err := session.run(ctx, chromedp.Evaluate(expression, &advanced, await)); err != nil {
		t.Fatalf("wait for playback to reach %.3fs: %v", target, err)
	}
	if !advanced {
		t.Fatal("player video disappeared while waiting for playback to advance")
	}
}

func waitForYouTubeAdapterPlayer(t *testing.T, ctx context.Context, session *targetSession, videoID string) youtubeAdapterPlayerOracle {
	t.Helper()
	for {
		oracle := inspectYouTubeAdapterPlayer(t, ctx, session)
		if oracle.Path == "/watch" && oracle.VideoID == videoID && !oracle.Paused && oracle.ReadyState >= 2 {
			return oracle
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for playing video %s: %v (last=%+v)", videoID, ctx.Err(), oracle)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func youtubeAdapterToneWAV(duration time.Duration, sampleRate int, frequency float64) []byte {
	samples := make([]int16, int(duration.Seconds()*float64(sampleRate)))
	for index := range samples {
		samples[index] = int16(math.Sin(2*math.Pi*frequency*float64(index)/float64(sampleRate)) * 6000)
	}
	var encoded bytes.Buffer
	if err := wavio.Write(&encoded, sampleRate, samples); err != nil {
		panic(fmt.Sprintf("encode youtube adapter tone: %v", err))
	}
	return encoded.Bytes()
}

const youtubeAdapterFixtureHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>YouTube adapter fixture</title>
<style>textarea,button,a,video{display:block;width:320px;height:40px;margin:10px}video{height:180px}</style></head>
<body><textarea id="search" name="search_query"></textarea><button aria-label="Search">Search</button><ytd-search id="content"></ytd-search>
<script>
document.addEventListener("click", (event) => {
  const button = event.target.closest("button[aria-label='Search']");
  if (button) {
    event.preventDefault();
    history.pushState({}, "", "/results?search_query=" + encodeURIComponent(document.querySelector("#search").value));
    document.querySelector("#content").innerHTML = '<ad-button-view-model><a aria-label="Sponsored video" href="/watch?v=advert123456">Sponsored video</a></ad-button-view-model><yt-lockup-view-model><a aria-label="Fixture tone 4 seconds" href="/watch?v=tone1234567">Fixture tone</a></yt-lockup-view-model>';
    return;
  }
  const anchor = event.target.closest("a[href*='/watch?v=']");
  if (anchor) {
    event.preventDefault();
    history.pushState({}, "", anchor.getAttribute("href"));
	document.body.innerHTML = '<h1 class="title">Fixture tone</h1><video src="/tone.wav"></video><button class="ytp-subtitles-button" aria-pressed="false" aria-disabled="false">CC</button>';
	document.querySelector("video").play();
  }
});
</script></body></html>`

type xPreparedReply struct {
	Data struct {
		Token string `json:"draft_token"`
		Text  string `json:"text"`
	} `json:"data"`
}

func testXAdapterJourney(t *testing.T) {
	source, _ := siteadapter.Source(siteadapter.XName)
	handler := func(writer http.ResponseWriter, _ *http.Request) {
		adapterFixtureHeaders(writer)
		if _, err := fmt.Fprint(writer, xAdapterFixtureHTML); err != nil {
			t.Errorf("write X fixture: %v", err)
		}
	}
	fixture := newAdapterFixture(t, "x", "https://x.com/home", source, `if (location.protocol !== "https:" || !ALLOWED_HOSTS.has(location.hostname.toLowerCase())) return;`, handler)

	contextOutput := invokeAdapterTool(t, fixture, "x_get_context", `{}`)
	if !strings.Contains(string(contextOutput), `"signed_in":true`) || !strings.Contains(string(contextOutput), `"account_handle":"@fixture_user"`) {
		t.Fatalf("X context = %s", contextOutput)
	}
	preparedOutput := invokeAdapterTool(t, fixture, "x_prepare_post", `{"text":"this is a test of the webmcp connection"}`)
	var prepared xPreparedReply
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
	testXAdapterPendingMedia(t, fixture)
	testXAdapterVideoJourney(t, fixture)
	t.Logf("WEBMCP_X_ADAPTER_PASS chrome=%s one_use_publish=true", fixture.version)
}

func testXAdapterPendingMedia(t *testing.T, fixture adapterFixture) {
	t.Helper()
	ctx, cancel := context.WithDeadline(t.Context(), fixture.deadline)
	defer cancel()
	t.Helper()
	var prepared xPreparedReply
	// File selection can precede its preview while Post is still enabled.
	pending := invokeAdapterTool(t, fixture, "x_prepare_post", `{"text":"text only pending guard"}`)
	if err := json.Unmarshal(pending, &prepared); err != nil || prepared.Data.Token == "" {
		t.Fatalf("pending draft=%s error=%v", pending, err)
	}
	var pendingState bool
	if err := fixture.target.run(ctx, chromedp.Evaluate(`(() => {
      window.deferMediaPreview = true;
      window.addPendingFile = () => {
        const input = document.querySelector('input[type="file"]');
        const transfer = new DataTransfer();
        transfer.items.add(new File(['pending'], 'pending.mp4', {type:'video/mp4'}));
        input.files = transfer.files;
        input.dispatchEvent(new Event('change', {bubbles:true}));
      };
      window.addPendingFile();
      return !document.querySelector('video') && !document.querySelector('[data-testid="tweetButtonInline"]').disabled;
    })()`, &pendingState)); err != nil || !pendingState {
		t.Fatalf("pending fixture state=%v error=%v", pendingState, err)
	}
	requireAdapterFailure(t, fixture, "x_publish_post", fmt.Sprintf(`{"draft_token":%q,"text":"text only pending guard","confirm":true}`, prepared.Data.Token), "media_changed")
	var ignored any
	if err := fixture.target.run(ctx, chromedp.Evaluate(`document.querySelector('input[type="file"]').value = ''`, &ignored)); err != nil {
		t.Fatal(err)
	}
	invokeAdapterTool(t, fixture, "x_clear_draft", fmt.Sprintf(`{"draft_token":%q}`, prepared.Data.Token))
	// Inject media synchronously on caption entry, after preparation's initial
	// media check and before its delayed verification.
	if err := fixture.target.run(ctx, chromedp.Evaluate(`document.querySelector('[data-testid="tweetTextarea_0"]').addEventListener('input', () => window.addPendingFile(), {once:true})`, &ignored)); err != nil {
		t.Fatal(err)
	}
	requireAdapterFailure(t, fixture, "x_prepare_post", `{"text":"media during preparation"}`, "existing_media")
	if err := fixture.target.run(ctx, chromedp.Evaluate(`document.querySelector('input[type="file"]').value=''; document.querySelector('[data-testid="tweetTextarea_0"]').textContent=''; window.deferMediaPreview=false`, &ignored)); err != nil {
		t.Fatal(err)
	}
}

func testXAdapterVideoJourney(t *testing.T, fixture adapterFixture) {
	t.Helper()
	ctx, cancel := context.WithDeadline(t.Context(), fixture.deadline)
	defer cancel()
	t.Helper()
	var prepared xPreparedReply
	var ignored any
	// The fixture accepts File objects but does not contact X. Exercise the
	// production transfer, hashing, composer, and one-use confirmation code.
	video := []byte("\x00\x00\x00\x18ftypisomfixture-video")
	hash := fmt.Sprintf("%x", sha256.Sum256(video))
	begin := fmt.Sprintf(`{"filename":"clip.mp4","size":%d,"sha256":%q,"account":"@fixture_user"}`, len(video), hash)
	requireAdapterFailure(t, fixture, "x_begin_video_upload", strings.Replace(begin, "@fixture_user", "@wrong", 1), "account_mismatch")
	started := invokeAdapterTool(t, fixture, "x_begin_video_upload", begin)
	var transfer struct {
		Data struct {
			Token string `json:"upload_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(started, &transfer); err != nil || transfer.Data.Token == "" {
		t.Fatalf("begin=%s error=%v", started, err)
	}
	token := transfer.Data.Token
	requireAdapterFailure(t, fixture, "x_begin_video_upload", begin, "upload_in_progress")
	requireAdapterFailure(t, fixture, "x_prepare_video_post", fmt.Sprintf(`{"upload_token":%q,"text":"video test"}`, token), "incomplete_upload")
	chunk := fmt.Sprintf(`{"upload_token":%q,"offset":0,"data_base64":%q}`, token, base64.StdEncoding.EncodeToString(video))
	requireAdapterFailure(t, fixture, "x_append_video_chunk", strings.Replace(chunk, `"offset":0`, `"offset":1`, 1), "chunk_order")
	invokeAdapterTool(t, fixture, "x_append_video_chunk", chunk)
	requireAdapterFailure(t, fixture, "x_append_video_chunk", chunk, "chunk_order")
	videoPrepared := invokeAdapterTool(t, fixture, "x_prepare_video_post", fmt.Sprintf(`{"upload_token":%q,"text":"video test"}`, token))
	if err := json.Unmarshal(videoPrepared, &prepared); err != nil || prepared.Data.Token == "" {
		t.Fatalf("video prepare=%s error=%v", videoPrepared, err)
	}
	if !strings.Contains(string(videoPrepared), hash) {
		t.Fatalf("missing verified hash: %s", videoPrepared)
	}
	requireAdapterFailure(t, fixture, "x_prepare_post", `{"text":"unrelated"}`, "existing_media")
	requireAdapterFailure(t, fixture, "x_clear_draft", fmt.Sprintf(`{"draft_token":%q}`, prepared.Data.Token), "manual_clear_required")
	if err := fixture.target.run(ctx, chromedp.Evaluate(`document.querySelector('[data-testid="AppTabBar_Profile_Link"]').href='/wrong'`, &ignored)); err != nil {
		t.Fatal(err)
	}
	publishVideo := fmt.Sprintf(`{"draft_token":%q,"text":"video test","confirm":true}`, prepared.Data.Token)
	requireAdapterFailure(t, fixture, "x_publish_post", publishVideo, "account_mismatch")
	if err := fixture.target.run(ctx, chromedp.Evaluate(`document.querySelector('[data-testid="AppTabBar_Profile_Link"]').href='/fixture_user'; document.querySelector('video').src='blob:changed'`, &ignored)); err != nil {
		t.Fatal(err)
	}
	requireAdapterFailure(t, fixture, "x_publish_post", publishVideo, "media_changed")
	if err := fixture.target.run(ctx, chromedp.Evaluate(`document.querySelector('video').src='blob:fixture-video'`, &ignored)); err != nil {
		t.Fatal(err)
	}
	invokeAdapterTool(t, fixture, "x_publish_post", publishVideo)
	requireAdapterFailure(t, fixture, "x_publish_post", publishVideo, "already_published")
	started = invokeAdapterTool(t, fixture, "x_begin_video_upload", strings.Replace(begin, hash, strings.Repeat("0", 64), 1))
	if err := json.Unmarshal(started, &transfer); err != nil {
		t.Fatal(err)
	}
	chunk = strings.Replace(chunk, token, transfer.Data.Token, 1)
	invokeAdapterTool(t, fixture, "x_append_video_chunk", chunk)
	requireAdapterFailure(t, fixture, "x_prepare_video_post", fmt.Sprintf(`{"upload_token":%q,"text":"bad hash"}`, transfer.Data.Token), "hash_mismatch")
	invokeAdapterTool(t, fixture, "x_cancel_video_upload", fmt.Sprintf(`{"upload_token":%q}`, transfer.Data.Token))
}
