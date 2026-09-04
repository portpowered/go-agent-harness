// Package siteadapter owns first-party page adapters injected by the Chrome
// WebMCP runtime. Adapters execute only on narrowly matched origins and keep
// browser authority inside the existing broker and target session.
package siteadapter

import (
	_ "embed"
	"net/url"
	"strings"
)

const YouTubeName = "youtube"

//go:embed extensions/youtube/youtube.js
var youtubeSource string

// Script is a main-world page adapter selected for one target URL.
type Script struct {
	Name   string
	Source string
}

// ForURL returns the default-on adapter for supported YouTube links. The
// youtu.be host is included because it redirects into youtube.com; the script
// is installed before that navigation and its own origin guard executes it
// only after the redirect reaches a supported YouTube document.
func ForURL(rawURL string) (Script, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return Script{}, false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be":
		return Script{Name: YouTubeName, Source: youtubeSource}, true
	default:
		return Script{}, false
	}
}

// YouTubeSource returns the embedded adapter for hermetic adapter tests. The
// production selection boundary remains ForURL.
func YouTubeSource() string {
	return youtubeSource
}

// NeedsTrustedActivation reports whether an admitted site-adapter invocation
// needs Chrome's user-gesture scope. It is deliberately limited to media
// actions that can otherwise be rejected by autoplay policy.
func NeedsTrustedActivation(rawURL, toolName string) bool {
	script, ok := ForURL(rawURL)
	if !ok || script.Name != YouTubeName {
		return false
	}
	return toolName == "youtube_play_video" || toolName == "youtube_resume"
}
