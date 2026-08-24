package main

import (
	"strings"
	"testing"
)

func TestVerifyEventsAcceptsExactlyOneNonSkippedCompletion(t *testing.T) {
	var output strings.Builder
	for _, testName := range requiredTests {
		output.WriteString(`{"Action":"run","Package":"example/sessions","Test":"`)
		output.WriteString(testName)
		output.WriteString(`"}` + "\n")
		output.WriteString(`{"Action":"pass","Package":"example/sessions","Test":"`)
		output.WriteString(testName)
		output.WriteString(`"}` + "\n")
	}
	if err := verifyEvents(strings.NewReader(output.String())); err != nil {
		t.Fatalf("verifyEvents() error = %v", err)
	}
}

func TestVerifyEventsRejectsMissingRequiredTest(t *testing.T) {
	input := `{"Action":"pass","Package":"example/sessions","Test":"TestConcurrentSessionsCompleteScriptedTurns"}` + "\n"
	err := verifyEvents(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "missing required tests") || !strings.Contains(err.Error(), "TestCancellingOneMidRunSessionLeavesOthersUndisturbed") {
		t.Fatalf("verifyEvents() error = %v, want missing-test diagnostic", err)
	}
}

func TestVerifyEventsRejectsSkippedRequiredTest(t *testing.T) {
	input := `{"Action":"skip","Package":"example/sessions","Test":"TestSharedCaptureBufferAliasingFailsIsolationCheck"}` + "\n"
	err := verifyEvents(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "skipped required tests") || !strings.Contains(err.Error(), "TestSharedCaptureBufferAliasingFailsIsolationCheck") {
		t.Fatalf("verifyEvents() error = %v, want skipped-test diagnostic", err)
	}
}

func TestVerifyEventsRejectsDuplicateCompletion(t *testing.T) {
	var input strings.Builder
	for _, action := range []string{"pass", "pass"} {
		input.WriteString(`{"Action":"` + action + `","Package":"example/sessions","Test":"TestConcurrentSessionsZeroCrossSessionLeakage"}` + "\n")
	}
	err := verifyEvents(strings.NewReader(input.String()))
	if err == nil || !strings.Contains(err.Error(), "completed more than once") {
		t.Fatalf("verifyEvents() error = %v, want duplicate-completion diagnostic", err)
	}
}

func TestVerifyEventsRejectsMalformedOutput(t *testing.T) {
	err := verifyEvents(strings.NewReader("go test failed before JSON output\n"))
	if err == nil || !strings.Contains(err.Error(), "decode go test JSON event") {
		t.Fatalf("verifyEvents() error = %v, want malformed-output diagnostic", err)
	}
}
