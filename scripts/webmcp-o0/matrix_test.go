package main

import "testing"

func TestTypedCoverageMatchesAdvertisedWebMCP(t *testing.T) {
	report := typedCoverage(advertisedProtocolReport{
		Available: true,
		Methods:   []string{"enable", "disable", "invokeTool", "cancelInvocation"},
		Events:    []string{"toolsAdded", "toolsRemoved", "toolInvoked", "toolResponded"},
	})

	if report.Verdict != "complete typed coverage" {
		t.Fatalf("coverage verdict = %q, want complete typed coverage", report.Verdict)
	}
	if len(report.MissingCommands) != 0 || len(report.MissingEvents) != 0 {
		t.Fatalf("coverage missing commands/events = %v/%v", report.MissingCommands, report.MissingEvents)
	}
}

func TestTypedCoverageReportsMissingRuntimeSurface(t *testing.T) {
	report := typedCoverage(advertisedProtocolReport{
		Available: true,
		Methods:   []string{"enable"},
		Events:    []string{"toolsAdded"},
	})

	if report.Verdict != "partial typed coverage" {
		t.Fatalf("coverage verdict = %q, want partial typed coverage", report.Verdict)
	}
	if len(report.MissingCommands) != 3 || len(report.MissingEvents) != 3 {
		t.Fatalf("coverage missing commands/events = %v/%v, want 3/3", report.MissingCommands, report.MissingEvents)
	}
}

func TestNativeVerdictRequiresNativePageAndCDPInvocation(t *testing.T) {
	pageReport := pageProbeReport{
		DocumentModelContext: pageObjectReport{Present: true},
		Fixture: fixtureStateReport{
			Registration: fixtureRegistrationReport{Outcome: "registered"},
		},
		ProducerDiscovery:  pageOperationReport{Outcome: "success"},
		ProducerInvocation: pageOperationReport{Outcome: "success"},
	}
	cdpReport := cdpProbeReport{
		Advertised: advertisedProtocolReport{Available: true},
		Typed:      typedCoverageReport{Verdict: "complete typed coverage"},
		Enable:     cdpAttemptReport{Outcome: "success"},
		Invocation: cdpInvocationReport{Outcome: "response", Status: "Completed"},
	}

	if got := nativeVerdict(pageReport, cdpReport); got != "PASS" {
		t.Fatalf("native verdict = %q, want PASS", got)
	}
	cdpReport.Invocation.Outcome = "error"
	if got := nativeVerdict(pageReport, cdpReport); got != "PARTIAL" {
		t.Fatalf("native verdict without CDP response = %q, want PARTIAL", got)
	}
}
