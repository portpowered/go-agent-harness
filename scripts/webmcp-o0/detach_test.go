package main

import "testing"

func TestDetachTransition(t *testing.T) {
	tests := []struct {
		phase    string
		expected string
		next     string
	}{
		{phase: "initial", expected: "initial", next: "attached"},
		{phase: "reattach", expected: "attached", next: "reattached"},
	}

	for _, test := range tests {
		t.Run(test.phase, func(t *testing.T) {
			expected, next, err := detachTransition(test.phase)
			if err != nil {
				t.Fatalf("detachTransition(%q): %v", test.phase, err)
			}
			if expected != test.expected || next != test.next {
				t.Fatalf("detachTransition(%q) = %q, %q; want %q, %q", test.phase, expected, next, test.expected, test.next)
			}
		})
	}
}

func TestDetachTransitionRejectsUnknownPhase(t *testing.T) {
	if _, _, err := detachTransition("unexpected"); err == nil {
		t.Fatal("detachTransition unexpectedly accepted an unknown phase")
	}
}

func TestIsLoopbackFixtureURL(t *testing.T) {
	valid := []string{
		"http://127.0.0.1:12345/",
		"http://127.0.0.1:1/",
	}
	for _, value := range valid {
		if !isLoopbackFixtureURL(value) {
			t.Errorf("isLoopbackFixtureURL(%q) = false, want true", value)
		}
	}

	invalid := []string{
		"https://127.0.0.1:12345/",
		"http://localhost:12345/",
		"http://127.0.0.1:12345/other",
		"http://127.0.0.1:12345/?external=1",
	}
	for _, value := range invalid {
		if isLoopbackFixtureURL(value) {
			t.Errorf("isLoopbackFixtureURL(%q) = true, want false", value)
		}
	}
}
