package chrome

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/siteadapter"
)

// Opt-in real media proof: unlike the synthetic guard fixture, this requires
// Chrome to decode a caller-supplied MP4 before preparation can finish.
func TestXAdapterRealMP4Decode(t *testing.T) {
	path := os.Getenv("WEBMCP_X_VIDEO_FILE")
	if os.Getenv(xAdapterIntegrationEnv) != "1" || path == "" {
		t.Skip("set WEBMCP_X_ADAPTER_INTEGRATION=1 and WEBMCP_X_VIDEO_FILE to an MP4")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024*1024 {
		t.Fatal("expected bounded regular video file")
	}
	video, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source, _ := siteadapter.Source(siteadapter.XName)
	html := strings.Replace(xAdapterFixtureHTML, "src=\"blob:fixture-video\"", "", 1)
	html = strings.Replace(html, "</video></div>';", "</video></div>'; const v=document.querySelector('video'); const button=document.querySelector('[data-testid=\"tweetButtonInline\"]');button.disabled=true;v.onloadedmetadata=()=>{button.disabled=false;};v.src=URL.createObjectURL(event.target.files[0]);v.load();", 1)
	fixture := newAdapterFixture(t, "x", "https://x.com/home", source, `if (location.protocol !== "https:" || !ALLOWED_HOSTS.has(location.hostname.toLowerCase())) return;`, func(w http.ResponseWriter, _ *http.Request) { adapterFixtureHeaders(w); fmt.Fprint(w, html) })
	release, err := fixture.target.AcquirePageFocus(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := release(fixture.ctx); err != nil {
			t.Error(err)
		}
	}()
	started := invokeAdapterTool(t, fixture, "x_begin_video_upload", fmt.Sprintf(`{"filename":"video.mp4","size":%d,"sha256":"%x","account":"@fixture_user"}`, len(video), sha256.Sum256(video)))
	var reply struct {
		Data struct {
			UploadToken string `json:"upload_token"`
			DraftToken  string `json:"draft_token"`
			Processing  bool   `json:"video_processing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(started, &reply); err != nil || reply.Data.UploadToken == "" {
		t.Fatalf("begin=%s err=%v", started, err)
	}
	token := reply.Data.UploadToken
	for offset := 0; offset < len(video); offset += 32768 {
		invokeAdapterTool(t, fixture, "x_append_video_chunk", fmt.Sprintf(`{"upload_token":%q,"offset":%d,"data_base64":%q}`, token, offset, base64.StdEncoding.EncodeToString(video[offset:min(offset+32768, len(video))])))
	}
	for {
		output := invokeAdapterTool(t, fixture, "x_prepare_video_post", fmt.Sprintf(`{"upload_token":%q,"text":"Real media decode test; never published"}`, token))
		reply.Data.DraftToken = ""
		if err := json.Unmarshal(output, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Data.DraftToken != "" {
			t.Logf("WEBMCP_X_REAL_MP4_PASS bytes=%d sha256=%x chrome=%s", len(video), sha256.Sum256(video), fixture.version)
			return
		}
		select {
		case <-fixture.ctx.Done():
			t.Fatal("real MP4 never became ready")
		case <-time.After(time.Second):
		}
	}
}
