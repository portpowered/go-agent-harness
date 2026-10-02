package openailive

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Every chunk fits the token limit, and the chunks rejoin to the content:
// cuts fall at sentence ends, then spaces, and only text with neither (a
// long word, or a script written without spaces) is cut mid-run, never
// mid-rune.
func TestSplitAppendContentKeepsEveryChunkUnderTheLimit(t *testing.T) {
	cases := []struct {
		name    string
		content string
		join    string
	}{
		{name: "sentences", content: strings.Repeat("One short sentence. ", 40), join: " "},
		{name: "words", content: strings.Repeat("word ", 400), join: " "},
		{name: "one long word", content: strings.Repeat("x", 4000), join: ""},
		{name: "no spaces", content: strings.Repeat("東京の天気は晴れです。", 120), join: ""},
	}
	const limit = 100
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := splitAppendContent(tc.content, limit)
			if len(chunks) < 2 {
				t.Fatalf("got %d chunks, want the content split", len(chunks))
			}
			for i, chunk := range chunks {
				if tokens := estimateTokens(chunk); tokens > limit || chunk == "" {
					t.Fatalf("chunk %d has %d tokens (%q), want 1..%d", i, tokens, chunk, limit)
				}
				if !utf8.ValidString(chunk) {
					t.Fatalf("chunk %d is not valid UTF-8", i)
				}
			}
			if got := strings.Join(chunks, tc.join); got != strings.TrimSpace(tc.content) {
				t.Fatalf("chunks rejoin to %d bytes, want the %d-byte content", len(got), len(strings.TrimSpace(tc.content)))
			}
			if tc.name == "sentences" && !strings.HasSuffix(chunks[0], ".") {
				t.Fatalf("first chunk %q does not end at a sentence", chunks[0])
			}
		})
	}
}

// Content within the limit is one append, trimmed.
func TestSplitAppendContentLeavesShortContentWhole(t *testing.T) {
	if got := splitAppendContent("  Booked for seven.  ", MaxAppendTokens); len(got) != 1 || got[0] != "Booked for seven." {
		t.Fatalf("chunks = %q, want one trimmed chunk", got)
	}
}

// A limit that admits no rune still makes progress, one rune per chunk.
func TestSplitAppendContentProgressesUnderAZeroLimit(t *testing.T) {
	if got := splitAppendContent("abc", 0); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("chunks = %q, want one rune each", got)
	}
}
