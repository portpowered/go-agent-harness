package normalize

import (
	"strings"
	"testing"
)

func TestRedactedOriginReducesURLsAndBoundsFallbackText(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "HTTPS://Page.Test:8443/a?b=1#c", want: "https://page.test:8443"},
		{raw: "https://page.test", want: "https://page.test"},
		{raw: "page.test/path?secret=1", want: "page.test/path"},
		{raw: "opaque#fragment", want: "opaque"},
		{raw: "bad\x01value", want: "badvalue"},
		{raw: "", want: ""},
	}
	for _, testCase := range cases {
		if got := RedactedOrigin(testCase.raw); got != testCase.want {
			t.Fatalf("RedactedOrigin(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
	if got := RedactedOrigin(strings.Repeat("a", 250)); len(got) != maxFallbackOriginLength {
		t.Fatalf("fallback origin length = %d, want %d", len(got), maxFallbackOriginLength)
	}
}

func TestBoundedTextDropsControlsAndTruncates(t *testing.T) {
	if got := BoundedText("a\x01b\tc\nd\re", 0); got != "ab\tc\nd\re" {
		t.Fatalf("BoundedText unbounded = %q", got)
	}
	if got := BoundedText("abcdef", 3); got != "abc" {
		t.Fatalf("BoundedText truncated = %q", got)
	}
}
