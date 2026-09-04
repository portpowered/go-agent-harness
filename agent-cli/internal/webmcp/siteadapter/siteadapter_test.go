package siteadapter

import (
	"strings"
	"testing"
)

func TestForURLSelectsOnlyHTTPSYouTubeLinks(t *testing.T) {
	for _, rawURL := range []string{
		"https://youtube.com/",
		"https://www.youtube.com/watch?v=abc123",
		"https://m.youtube.com/results?search_query=test",
		"https://youtu.be/abc123",
	} {
		script, ok := ForURL(rawURL)
		if !ok || script.Name != YouTubeName || script.Source == "" {
			t.Fatalf("ForURL(%q) = %+v, %v", rawURL, script, ok)
		}
	}

	for _, rawURL := range []string{
		"http://www.youtube.com/",
		"https://youtube.com.example.test/",
		"https://example.test/?next=https://www.youtube.com/",
		"javascript:alert(1)",
		"",
	} {
		if _, ok := ForURL(rawURL); ok {
			t.Fatalf("ForURL(%q) selected an adapter", rawURL)
		}
	}
}

func TestYouTubeScriptIsOriginGatedAndRegistersStableTools(t *testing.T) {
	script := YouTubeSource()
	for _, host := range []string{"youtube.com", "www.youtube.com", "m.youtube.com"} {
		if !strings.Contains(script, `"`+host+`"`) {
			t.Errorf("script omits origin gate for %s", host)
		}
	}
	for _, tool := range []string{"youtube_get_context", "youtube_search", "youtube_list_results", "youtube_play_video", "youtube_get_player_state", "youtube_pause", "youtube_resume", "youtube_seek", "youtube_set_volume", "youtube_set_captions"} {
		if strings.Count(script, `name: "`+tool+`"`) != 1 {
			t.Errorf("tool %s is not registered exactly once", tool)
		}
	}
}

func TestNeedsTrustedActivationIsNarrowlyScoped(t *testing.T) {
	if !NeedsTrustedActivation("https://www.youtube.com/watch?v=abc123", "youtube_play_video") {
		t.Fatal("YouTube play does not request trusted activation")
	}
	if !NeedsTrustedActivation("https://youtube.com/watch?v=abc123", "youtube_resume") {
		t.Fatal("YouTube resume does not request trusted activation")
	}
	for _, candidate := range []struct{ rawURL, tool string }{
		{"https://example.com/", "youtube_play_video"},
		{"http://www.youtube.com/", "youtube_play_video"},
		{"https://www.youtube.com/", "youtube_search"},
		{"https://www.youtube.com/", "youtube_pause"},
	} {
		if NeedsTrustedActivation(candidate.rawURL, candidate.tool) {
			t.Fatalf("NeedsTrustedActivation(%q, %q) = true", candidate.rawURL, candidate.tool)
		}
	}
}
