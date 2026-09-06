// Package siteadapter owns first-party page adapters injected by the Chrome
// WebMCP runtime. Adapters execute only on narrowly matched origins and keep
// browser authority inside the existing broker and target session.
package siteadapter

import (
	_ "embed"
	"net/url"
	"strings"
)

const (
	YouTubeName            = "youtube"
	SpotifyName            = "spotify"
	WikipediaName          = "wikipedia"
	RedditName             = "reddit"
	GoogleMapsName         = "google_maps"
	CapitalOneShoppingName = "capital_one_shopping"
	XName                  = "x"
)

//go:embed extensions/youtube/youtube.js
var youtubeSource string

//go:embed extensions/spotify/spotify.js
var spotifySource string

//go:embed extensions/wikipedia/wikipedia.js
var wikipediaSource string

//go:embed extensions/reddit/reddit.js
var redditSource string

//go:embed extensions/google_maps/google_maps.js
var googleMapsSource string

//go:embed extensions/capital_one_shopping/capital_one_shopping.js
var capitalOneShoppingSource string

//go:embed extensions/x/x.js
var xSource string

// Script is a main-world page adapter selected for one target URL.
type Script struct {
	Name   string
	Source string
}

// Info is stable, customer-facing metadata for a bundled adapter.
type Info struct {
	Name        string
	URLPatterns []string
	ToolPrefix  string
}

type definition struct {
	info              Info
	source            string
	match             func(*url.URL) bool
	trustedActivation map[string]struct{}
}

var registry = []definition{
	{
		info:   Info{Name: YouTubeName, URLPatterns: []string{"https://youtube.com/*", "https://www.youtube.com/*", "https://m.youtube.com/*", "https://youtu.be/*"}, ToolPrefix: "youtube_"},
		source: youtubeSource,
		match: func(parsed *url.URL) bool {
			switch strings.ToLower(parsed.Hostname()) {
			case "youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be":
				return true
			default:
				return false
			}
		},
		trustedActivation: stringSet("youtube_play_video", "youtube_resume"),
	},
	{
		info:              Info{Name: SpotifyName, URLPatterns: []string{"https://open.spotify.com/*"}, ToolPrefix: "spotify_"},
		source:            spotifySource,
		match:             func(parsed *url.URL) bool { return strings.EqualFold(parsed.Hostname(), "open.spotify.com") },
		trustedActivation: stringSet("spotify_play_track", "spotify_resume"),
	},
	{
		info:   Info{Name: WikipediaName, URLPatterns: []string{"https://*.wikipedia.org/*"}, ToolPrefix: "wikipedia_"},
		source: wikipediaSource,
		match: func(parsed *url.URL) bool {
			host := strings.ToLower(parsed.Hostname())
			return host == "wikipedia.org" || strings.HasSuffix(host, ".wikipedia.org")
		},
	},
	{
		info:   Info{Name: RedditName, URLPatterns: []string{"https://reddit.com/*", "https://www.reddit.com/*", "https://old.reddit.com/*"}, ToolPrefix: "reddit_"},
		source: redditSource,
		match: func(parsed *url.URL) bool {
			switch strings.ToLower(parsed.Hostname()) {
			case "reddit.com", "www.reddit.com", "old.reddit.com":
				return true
			default:
				return false
			}
		},
	},
	{
		info:   Info{Name: GoogleMapsName, URLPatterns: []string{"https://www.google.com/maps/*", "https://maps.google.com/*"}, ToolPrefix: "google_maps_"},
		source: googleMapsSource,
		match: func(parsed *url.URL) bool {
			host := strings.ToLower(parsed.Hostname())
			return host == "maps.google.com" || host == "www.google.com" && (parsed.Path == "/maps" || strings.HasPrefix(parsed.Path, "/maps/"))
		},
	},
	{
		info:   Info{Name: CapitalOneShoppingName, URLPatterns: []string{"https://capitaloneshopping.com/*", "https://www.capitaloneshopping.com/*"}, ToolPrefix: "capital_one_shopping_"},
		source: capitalOneShoppingSource,
		match: func(parsed *url.URL) bool {
			switch strings.ToLower(parsed.Hostname()) {
			case "capitaloneshopping.com", "www.capitaloneshopping.com":
				return true
			default:
				return false
			}
		},
	},
	{
		info:   Info{Name: XName, URLPatterns: []string{"https://x.com/*", "https://www.x.com/*", "https://twitter.com/*", "https://www.twitter.com/*"}, ToolPrefix: "x_"},
		source: xSource,
		match: func(parsed *url.URL) bool {
			switch strings.ToLower(parsed.Hostname()) {
			case "x.com", "www.x.com", "twitter.com", "www.twitter.com":
				return true
			default:
				return false
			}
		},
	},
}

var bootstrapSource = func() string {
	parts := make([]string, 0, len(registry))
	for _, adapter := range registry {
		parts = append(parts, adapter.source)
	}
	return strings.Join(parts, "\n")
}()

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

// ForURL returns the single default-on adapter matching an HTTPS target. Every
// matcher uses parsed host/path components rather than substring matching.
func ForURL(rawURL string) (Script, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil {
		return Script{}, false
	}
	for _, adapter := range registry {
		if adapter.match(parsed) {
			return Script{Name: adapter.info.Name, Source: adapter.source}, true
		}
	}
	return Script{}, false
}

// Supported returns a defensive copy of the bundled adapter registry.
func Supported() []Info {
	result := make([]Info, 0, len(registry))
	for _, adapter := range registry {
		info := adapter.info
		info.URLPatterns = append([]string(nil), info.URLPatterns...)
		result = append(result, info)
	}
	return result
}

// BootstrapSource returns the dispatcher installed in every attached target.
// Each bundled IIFE fails closed on its own exact HTTPS host/path boundary.
// Installing the dispatcher independently of the target's initial URL avoids
// races where a newly opened target is still about:blank during attachment.
func BootstrapSource() string {
	return bootstrapSource
}

// YouTubeSource returns the embedded adapter for hermetic adapter tests. The
// production selection boundary remains ForURL.
func YouTubeSource() string {
	return youtubeSource
}

// Source returns an embedded adapter by registry name for hermetic tests.
func Source(name string) (string, bool) {
	for _, adapter := range registry {
		if adapter.info.Name == name {
			return adapter.source, true
		}
	}
	return "", false
}

// NeedsTrustedActivation reports whether an admitted site-adapter invocation
// needs Chrome's user-gesture scope. It is deliberately limited to media
// actions that can otherwise be rejected by autoplay policy.
func NeedsTrustedActivation(rawURL, toolName string) bool {
	script, ok := ForURL(rawURL)
	if !ok {
		return false
	}
	for _, adapter := range registry {
		if adapter.info.Name != script.Name {
			continue
		}
		_, ok := adapter.trustedActivation[toolName]
		return ok
	}
	return false
}
