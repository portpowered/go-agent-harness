package main

import (
	"testing"
)

func TestExternalConsumerUsesIsolatedExecutableCapabilities(t *testing.T) {
	report, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != "audio-runtime.c88.roomcapabilities-consumer/v1" || report.ConstructedVia != "roomcapabilities/wire.NewService" {
		t.Fatalf("consumer identity = %#v", report)
	}
	if got, want := report.FirstInitialDefinitions, []string{"alpha", "page-first"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("first initial definitions = %v", got)
	}
	if got, want := report.FirstBaseDefinitions, []string{"alpha", "page-stable"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("first base definitions = %v", got)
	}
	if got, want := report.FirstRefreshed, []string{"alpha", "page-first-refreshed"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("first refreshed definitions = %v", got)
	}
	if got, want := report.SecondDefinitions, []string{"beta", "page-second"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("second definitions = %v", got)
	}
	for name, want := range map[string]string{
		"first static":                       "first-static:alpha",
		"first browser":                      "first-browser:page-first",
		"second static":                      "second-static:beta",
		"second browser":                     "second-browser:page-second",
		"first refreshed browser":            "first-browser:page-first-refreshed",
		"second browser after first refresh": "second-browser:page-second",
	} {
		if report.Dispatch[name] != want {
			t.Errorf("dispatch[%q] = %q, want %q", name, report.Dispatch[name], want)
		}
	}
	if !report.MismatchErrorIdentity || !report.StaleToolRejected || !report.StaticPreserved || !report.FirstInitializedOnce || !report.FirstClosedOnce || !report.SecondInitializedOnce || !report.SecondClosedOnce {
		t.Fatalf("consumer behavior = %#v", report)
	}
}
