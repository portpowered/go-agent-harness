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

func TestForURLSelectsEveryBundledAdapterAndRejectsLookalikes(t *testing.T) {
	for _, candidate := range []struct {
		rawURL string
		name   string
	}{
		{"https://open.spotify.com/search/test", SpotifyName},
		{"https://en.wikipedia.org/wiki/Web_browser", WikipediaName},
		{"https://www.wikipedia.org/", WikipediaName},
		{"https://www.reddit.com/search/?q=test", RedditName},
		{"https://old.reddit.com/r/golang/", RedditName},
		{"https://www.google.com/maps/dir/", GoogleMapsName},
		{"https://maps.google.com/", GoogleMapsName},
		{"https://capitaloneshopping.com/", CapitalOneShoppingName},
		{"https://www.capitaloneshopping.com/deals", CapitalOneShoppingName},
		{"https://x.com/home", XName},
		{"https://www.x.com/compose/post", XName},
		{"https://twitter.com/home", XName},
		{"https://www.twitter.com/example", XName},
	} {
		script, ok := ForURL(candidate.rawURL)
		if !ok || script.Name != candidate.name || script.Source == "" {
			t.Errorf("ForURL(%q) = %+v, %v; want %s", candidate.rawURL, script, ok, candidate.name)
		}
	}

	for _, rawURL := range []string{
		"http://open.spotify.com/",
		"https://open.spotify.com.example.test/",
		"https://spotify.com/",
		"https://wikipedia.org.example.test/",
		"https://notwikipedia.org/",
		"https://reddit.com.example.test/",
		"https://new.reddit.com/",
		"https://www.google.com/",
		"https://www.google.com/search?q=maps",
		"https://maps.google.com.example.test/",
		"https://www.google.com@evil.test/maps/",
		"http://capitaloneshopping.com/",
		"https://capitaloneshopping.com.example.test/",
		"https://www.capitaloneshopping.com@evil.test/",
		"http://x.com/home",
		"https://x.com.example.test/home",
		"https://twitter.com@evil.test/home",
	} {
		if script, ok := ForURL(rawURL); ok {
			t.Errorf("ForURL(%q) selected unexpected adapter %+v", rawURL, script)
		}
	}
}

func TestSupportedRegistryIsCompleteAndDefensive(t *testing.T) {
	infos := Supported()
	if len(infos) != 7 {
		t.Fatalf("Supported() has %d adapters, want 7", len(infos))
	}
	want := map[string]string{
		YouTubeName: "youtube_", SpotifyName: "spotify_", WikipediaName: "wikipedia_", RedditName: "reddit_", GoogleMapsName: "google_maps_", CapitalOneShoppingName: "capital_one_shopping_", XName: "x_",
	}
	for _, info := range infos {
		if want[info.Name] != info.ToolPrefix || len(info.URLPatterns) == 0 {
			t.Errorf("adapter metadata = %+v", info)
		}
		delete(want, info.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing registry entries: %v", want)
	}
	infos[0].URLPatterns[0] = "mutated"
	again := Supported()
	if again[0].URLPatterns[0] == "mutated" {
		t.Fatal("Supported returned mutable registry storage")
	}
}

func TestBootstrapSourceContainsEachAdapterExactlyOnce(t *testing.T) {
	bootstrap := BootstrapSource()
	if bootstrap == "" {
		t.Fatal("BootstrapSource is empty")
	}
	for _, info := range Supported() {
		if count := strings.Count(bootstrap, `const INSTALL_KEY = "__yui`+adapterInstallStem(info.Name)); count != 1 {
			t.Errorf("bootstrap install marker count for %s = %d, want 1", info.Name, count)
		}
	}
}

func adapterInstallStem(name string) string {
	switch name {
	case YouTubeName:
		return "YouTube"
	case SpotifyName:
		return "Spotify"
	case WikipediaName:
		return "Wikipedia"
	case RedditName:
		return "Reddit"
	case GoogleMapsName:
		return "GoogleMaps"
	case CapitalOneShoppingName:
		return "CapitalOneShopping"
	case XName:
		return "X"
	default:
		return "missing"
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

func TestEveryAdapterScriptHasAnOriginGateAndStableToolPrefix(t *testing.T) {
	for _, info := range Supported() {
		source, ok := Source(info.Name)
		if !ok || source == "" {
			t.Fatalf("Source(%q) missing", info.Name)
		}
		if !strings.Contains(source, `location.protocol !== "https:"`) && !strings.Contains(source, `location.protocol !== ALLOWED_PROTOCOL`) {
			t.Errorf("adapter %s has no in-script HTTPS gate", info.Name)
		}
		if count := strings.Count(source, `name: "`+info.ToolPrefix); count < 4 {
			t.Errorf("adapter %s registers only %d prefixed tools", info.Name, count)
		}
	}
}

func TestCapitalOneShoppingScriptHasBoundedReadOnlyScanContract(t *testing.T) {
	script, ok := Source(CapitalOneShoppingName)
	if !ok || script == "" {
		t.Fatal("Capital One Shopping adapter source is missing")
	}
	for _, host := range []string{"capitaloneshopping.com", "www.capitaloneshopping.com"} {
		if !strings.Contains(script, `"`+host+`"`) {
			t.Errorf("Capital One Shopping script omits origin gate for %s", host)
		}
	}
	for _, tool := range []string{
		"capital_one_shopping_get_context",
		"capital_one_shopping_scan_offers",
		"capital_one_shopping_list_matches",
		"capital_one_shopping_reset_scan",
	} {
		if strings.Count(script, `name: "`+tool+`"`) != 1 {
			t.Errorf("tool %s is not registered exactly once", tool)
		}
	}
	for _, required := range []string{
		"const MAX_PAGES = 20",
		"reward_cap_usd",
		"qualifying_spend_usd",
		"unknown_cost_policy",
		"This only reads and scrolls; it never activates an offer or makes a purchase.",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("Capital One Shopping scan contract omits %q", required)
		}
	}
}

func TestXScriptHasExactOneUsePublishContract(t *testing.T) {
	script, ok := Source(XName)
	if !ok || script == "" {
		t.Fatal("X adapter source is missing")
	}
	for _, host := range []string{"x.com", "www.x.com", "twitter.com", "www.twitter.com"} {
		if !strings.Contains(script, `"`+host+`"`) {
			t.Errorf("X script omits origin gate for %s", host)
		}
	}
	for _, tool := range []string{"x_get_context", "x_prepare_post", "x_publish_post", "x_clear_draft"} {
		if strings.Count(script, `name: "`+tool+`"`) != 1 {
			t.Errorf("tool %s is not registered exactly once", tool)
		}
	}
	for _, required := range []string{
		"const MAX_POST_LENGTH = 280",
		`const: true`,
		`text !== state.draftText`,
		`state.consumedTokens.add(token)`,
		`one native editing sequence`,
		`publish_status_unknown`,
		`Do not retry automatically`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("X publish contract omits %q", required)
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
	if !NeedsTrustedActivation("https://open.spotify.com/track/abc1234567", "spotify_play_track") {
		t.Fatal("Spotify play does not request trusted activation")
	}
	if !NeedsTrustedActivation("https://open.spotify.com/track/abc1234567", "spotify_resume") {
		t.Fatal("Spotify resume does not request trusted activation")
	}
	for _, candidate := range []struct{ rawURL, tool string }{
		{"https://example.com/", "youtube_play_video"},
		{"http://www.youtube.com/", "youtube_play_video"},
		{"https://www.youtube.com/", "youtube_search"},
		{"https://www.youtube.com/", "youtube_pause"},
		{"https://open.spotify.com/", "spotify_pause"},
		{"https://example.com/", "spotify_resume"},
		{"https://www.reddit.com/", "spotify_resume"},
	} {
		if NeedsTrustedActivation(candidate.rawURL, candidate.tool) {
			t.Fatalf("NeedsTrustedActivation(%q, %q) = true", candidate.rawURL, candidate.tool)
		}
	}
}
