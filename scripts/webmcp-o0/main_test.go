package main

import (
	"runtime"
	"testing"
)

func TestBindingSmoke(t *testing.T) {
	report := bindingSmoke()

	if report.GOOS != runtime.GOOS || report.GOARCH != runtime.GOARCH {
		t.Fatalf("reported platform = %s/%s, running platform = %s/%s", report.GOOS, report.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
	if report.ChromedpVersion != "v0.16.0" {
		t.Fatalf("chromedp pin = %q, want v0.16.0", report.ChromedpVersion)
	}
	if report.CDProtoVersion != "v0.0.0-20260714215040-dc233986426f" {
		t.Fatalf("cdproto pin = %q, want v0.0.0-20260714215040-dc233986426f", report.CDProtoVersion)
	}

	wantCommands := []string{
		"WebMCP.enable",
		"WebMCP.disable",
		"WebMCP.invokeTool",
		"WebMCP.cancelInvocation",
	}
	if len(report.Commands) != len(wantCommands) {
		t.Fatalf("generated command count = %d, want %d", len(report.Commands), len(wantCommands))
	}
	for index, want := range wantCommands {
		if report.Commands[index] != want {
			t.Errorf("generated command %d = %q, want %q", index, report.Commands[index], want)
		}
	}
	if len(report.EventTypes) != 4 {
		t.Fatalf("generated event type count = %d, want 4", len(report.EventTypes))
	}
	if report.Checks["generatedWebMCP"] != "constructed" {
		t.Fatalf("generated WebMCP check = %q", report.Checks["generatedWebMCP"])
	}
}
