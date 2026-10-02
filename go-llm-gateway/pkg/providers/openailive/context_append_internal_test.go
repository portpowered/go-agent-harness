package openailive

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// withoutSpace drops every whitespace rune, so chunks can be compared with
// the content they came from regardless of the whitespace trimmed at cuts.
func withoutSpace(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
}

// checkChunks asserts that every chunk is non-empty and within the byte
// bound, and that the chunks hold the content in order.
func checkChunks(t *testing.T, content string, chunks []string, limit int) {
	t.Helper()
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks for %d bytes, want the content split", len(chunks), len(content))
	}
	for i, chunk := range chunks {
		if chunk == "" || len(chunk) > limit {
			t.Fatalf("chunk %d is %d bytes, want 1..%d", i, len(chunk), limit)
		}
	}
	if got, want := withoutSpace(strings.Join(chunks, "")), withoutSpace(content); got != want {
		t.Fatalf("chunks hold %d non-space bytes, want the content's %d in order", len(got), len(want))
	}
}

// Content a per-character estimate undercounts (the reviewer measured up to
// 1126 o200k tokens in chunks estimated at 500) is still bounded: each chunk
// is at most MaxAppendTokens bytes, which byte-level BPE cannot exceed in
// tokens. Cuts never split a rune.
func TestSplitAppendContentBoundsAdversarialContentByBytes(t *testing.T) {
	pattern := make([]byte, 1500)
	for i := range pattern {
		pattern[i] = byte(i * 7)
	}
	cases := map[string]string{
		"json ids":    strings.Repeat(`{"id":"del_8f3a2c","n":42,"ok":true},`, 60),
		"paths":       strings.Repeat("/usr/local/lib/go/src/net/http/server.go:2071 ", 40),
		"digits":      strings.Repeat("0123456789", 160),
		"hex":         strings.Repeat("deadbeefcafebabe0123456789abcdef", 50),
		"uuids":       strings.Repeat("123e4567-e89b-12d3-a456-426614174000 ", 40),
		"base64":      base64.StdEncoding.EncodeToString(pattern),
		"punctuation": strings.Repeat(`!?.,;:'"()[]{}<>/\|@#$%^&*-_+=~`+"`", 50),
		"emoji":       strings.Repeat("😀🎉🚀👩‍💻", 80),
		"rare cjk":    strings.Repeat("𠀀𠀁𠀂龘靐齉爨", 80),
		"sentences":   strings.Repeat("One short sentence. ", 80),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			chunks := splitAppendContent(content, maxAppendBytes)
			checkChunks(t, content, chunks, maxAppendBytes)
			for i, chunk := range chunks {
				if !utf8.ValidString(chunk) {
					t.Fatalf("chunk %d splits a rune", i)
				}
			}
			if name == "sentences" && !strings.HasSuffix(chunks[0], ".") {
				t.Fatalf("first chunk %q does not end at a sentence", chunks[0])
			}
		})
	}
}

// Invalid UTF-8 just over the limit splits without a panic: an invalid byte
// is a one-byte rune, mixed with valid multi-byte runes or alone.
func TestSplitAppendContentSplitsInvalidUTF8(t *testing.T) {
	cases := map[string]string{
		"invalid only":   strings.Repeat("\xff", maxAppendBytes+1),
		"mixed":          strings.Repeat("é\xff日\xfe😀", 50),
		"truncated rune": strings.Repeat("ab", maxAppendBytes/2-1) + "\xe6\x97" + "日本語",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			checkChunks(t, content, splitAppendContent(content, maxAppendBytes), maxAppendBytes)
		})
	}
}

// Content within the limit is one append, trimmed.
func TestSplitAppendContentLeavesShortContentWhole(t *testing.T) {
	if got := splitAppendContent("  Booked for seven.  ", maxAppendBytes); len(got) != 1 || got[0] != "Booked for seven." {
		t.Fatalf("chunks = %q, want one trimmed chunk", got)
	}
}

// A limit smaller than the first rune still makes progress, one rune per
// chunk.
func TestSplitAppendContentProgressesUnderATinyLimit(t *testing.T) {
	if got := splitAppendContent("日本語", 2); len(got) != 3 || got[0] != "日" || got[2] != "語" {
		t.Fatalf("chunks = %q, want one rune each", got)
	}
}
