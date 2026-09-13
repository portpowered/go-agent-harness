package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const validatorProcessTestTimeout = 5 * time.Second

func TestC61ValidatorProcessHelper(t *testing.T) {
	switch os.Getenv("C61_VALIDATOR_MODE") {
	case "valid":
		writeC61ValidatorVerdict(t)
	case "nonzero":
		if _, err := os.Stderr.WriteString("validator failed for a causal test\n"); err != nil {
			t.Fatalf("write validator failure: %v", err)
		}
		os.Exit(9)
	case "malformed":
		if _, err := os.Stdout.WriteString("{"); err != nil {
			t.Fatalf("write malformed verdict: %v", err)
		}
	case "invalid":
		if _, err := os.Stdout.WriteString(`{"version":"` + BrowserConversationValidatorVersion + `","status":"pass","passed":false}`); err != nil {
			t.Fatalf("write invalid verdict: %v", err)
		}
	case "stdout-overflow":
		if _, err := os.Stdout.WriteString(strings.Repeat("x", (1<<20)+1)); err != nil {
			t.Fatalf("write stdout overflow: %v", err)
		}
	case "stderr-overflow":
		if _, err := os.Stderr.WriteString(strings.Repeat("x", (1<<20)+1)); err != nil {
			t.Fatalf("write stderr overflow: %v", err)
		}
		writeC61ValidatorVerdict(t)
	}
}

func writeC61ValidatorVerdict(t *testing.T) {
	verdict := BrowserConversationValidatorVerdict{Version: BrowserConversationValidatorVersion, Status: BrowserConversationValidatorPass, Passed: true}
	for _, name := range BrowserConversationValidatorRubric() {
		verdict.Checks = append(verdict.Checks, BrowserConversationValidatorCheck{Name: name, Passed: true})
	}
	if err := json.NewEncoder(os.Stdout).Encode(verdict); err != nil {
		t.Fatalf("write validator verdict: %v", err)
	}
}

func TestBrowserConversationCommandValidatorPreservesTypedProcessCauses(t *testing.T) {
	result := validatorProcessResultForTest()
	cases := []struct {
		name string
		mode string
		want error
	}{
		{name: "start", mode: "start", want: ErrBrowserConversationValidatorStart},
		{name: "nonzero", mode: "nonzero", want: ErrBrowserConversationValidatorFailed},
		{name: "malformed", mode: "malformed", want: ErrBrowserConversationValidatorVerdict},
		{name: "invalid", mode: "invalid", want: ErrBrowserConversationValidatorVerdict},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			command := []string{os.Args[0], "-test.run=^TestC61ValidatorProcessHelper$"}
			if test.mode == "start" {
				command = []string{"/definitely/missing/c61-validator"}
			}
			validator, err := NewBrowserConversationCommandValidator(command, validatorProcessTestTimeout)
			if err != nil {
				t.Fatalf("new validator: %v", err)
			}
			if test.mode != "start" {
				validator.Env = []string{"C61_VALIDATOR_MODE=" + test.mode}
			}
			_, err = validator.ValidateBrowserConversation(result)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.want)
			}
			if strings.Contains(err.Error(), "causal test") {
				t.Fatal("validator stderr escaped into the public error")
			}
		})
	}
}

func TestBrowserConversationCommandValidatorCapsStdoutAndStderrIndependently(t *testing.T) {
	result := validatorProcessResultForTest()
	for _, mode := range []string{"stdout-overflow", "stderr-overflow"} {
		t.Run(mode, func(t *testing.T) {
			validator, err := NewBrowserConversationCommandValidator([]string{os.Args[0], "-test.run=^TestC61ValidatorProcessHelper$"}, validatorProcessTestTimeout)
			if err != nil {
				t.Fatalf("new validator: %v", err)
			}
			validator.Env = []string{"C61_VALIDATOR_MODE=" + mode}
			_, err = validator.ValidateBrowserConversation(result)
			if !errors.Is(err, ErrBrowserConversationValidatorOutput) {
				t.Fatalf("error = %v, want bounded output cause", err)
			}
		})
	}
}

func TestBrowserConversationCommandValidatorTimesOutAndCleansProcessGroup(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	validator, err := NewBrowserConversationCommandValidator([]string{"/bin/sh", "-c", "trap '' TERM; sleep 10"}, 40*time.Millisecond)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	started := time.Now()
	_, err = validator.ValidateBrowserConversation(validatorProcessResultForTest())
	if !errors.Is(err, ErrBrowserConversationValidatorTimeout) {
		t.Fatalf("error = %v, want timeout cause", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout cleanup took %s, want bounded cleanup", elapsed)
	}
}

func TestBrowserConversationCommandValidatorRejectsCredentialBoundaryAndFiltersAmbientEnvironment(t *testing.T) {
	validator, err := NewBrowserConversationCommandValidator([]string{os.Args[0], "-test.run=^TestC61ValidatorProcessHelper$"}, validatorProcessTestTimeout)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	validator.Env = []string{"C61_TOKEN=sk-live-never-publish"}
	if _, err := validator.ValidateBrowserConversation(validatorProcessResultForTest()); !errors.Is(err, ErrBrowserConversationValidatorCommand) {
		t.Fatalf("credential environment error = %v, want command cause", err)
	}

	ambient := append([]string(nil), os.Environ()...)
	if err := os.Setenv("C61_SECRET_SHOULD_NOT_CROSS", "sk-live-never-publish"); err != nil {
		t.Fatalf("set ambient credential: %v", err)
	}
	defer func() {
		for _, entry := range ambient {
			name, _, ok := strings.Cut(entry, "=")
			if ok {
				if err := os.Setenv(name, strings.TrimPrefix(entry, name+"=")); err != nil {
					t.Errorf("restore environment %q: %v", name, err)
				}
			}
		}
		if err := os.Unsetenv("C61_SECRET_SHOULD_NOT_CROSS"); err != nil {
			t.Errorf("unset ambient credential: %v", err)
		}
	}()
	if got := browserConversationValidatorEnvironment(nil); slicesContainCredential(got) {
		t.Fatalf("ambient validator environment contains a credential marker: %v", got)
	}
}

func TestBrowserConversationReportRendersWritesAndSanitizesOpaqueValues(t *testing.T) {
	type secretRef string
	result := BrowserConversationResult{
		ScenarioID: "scenario", ScenarioName: "name",
		BrokerCalls: []BrowserConversationBrokerCall{{Sequence: 1, Operation: BrowserConversationInvoke, ToolRef: secretRef("Authorization: Bearer sk-live-never-publish"), InvocationID: secretRef("invocation"), State: secretRef("completed"), ToolRefs: []secretRef{"safe", "sk-live-never-publish"}, InputJSON: `{}`}},
	}
	report, err := NewBrowserConversationReport(result, BrowserConversationReportMetadata{})
	if err != nil {
		t.Fatalf("new report: %v", err)
	}
	encoded, err := MarshalBrowserConversationReport(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if bytes.Contains(encoded, []byte("sk-live-never-publish")) || bytes.Contains(encoded, []byte("Authorization: Bearer")) {
		t.Fatalf("report leaked opaque credential: %s", encoded)
	}
	refs, ok := report.Evidence.BrokerCalls[0].ToolRefs.([]secretRef)
	if !ok || refs[0] != "safe" || refs[1] != browserConversationTestRedactedText {
		t.Fatalf("sanitized named refs = %#v, want shape-preserving redaction", report.Evidence.BrokerCalls[0].ToolRefs)
	}
	rendered, err := RenderBrowserConversationReport(result, BrowserConversationReportMetadata{Provider: "provider"})
	if err != nil || !strings.Contains(rendered, "```json") {
		t.Fatalf("rendered report = %q, err=%v", rendered, err)
	}
	var output bytes.Buffer
	if err := WriteBrowserConversationReport(&output, result, BrowserConversationReportMetadata{}); err != nil || output.Len() == 0 {
		t.Fatalf("written report length=%d, err=%v", output.Len(), err)
	}
	if err := WriteBrowserConversationReport(nil, result, BrowserConversationReportMetadata{}); err == nil {
		t.Fatal("nil report writer was accepted")
	}
}

func TestBrowserConversationBoundedBufferStopsGrowingAfterLimit(t *testing.T) {
	buffer := browserConversationBoundedBuffer{limit: 3}
	if _, err := buffer.Write([]byte("abcd")); err != nil || !buffer.truncated || buffer.String() != "abc" {
		t.Fatalf("bounded buffer = %q truncated=%t err=%v", buffer.String(), buffer.truncated, err)
	}
	if _, err := buffer.Write([]byte("efgh")); err != nil || buffer.Len() != 3 {
		t.Fatalf("bounded buffer grew after truncation: %q err=%v", buffer.String(), err)
	}
}

func validatorProcessResultForTest() BrowserConversationResult {
	return BrowserConversationResult{
		ScenarioID: "scenario", ScenarioName: "name",
		BrokerCalls: []BrowserConversationBrokerCall{{Sequence: 1, Operation: BrowserConversationInvoke, InputJSON: `{}`, State: "completed", Terminal: true}},
	}
}

func slicesContainCredential(values []string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), "c61_secret") || strings.Contains(value, "sk-live-never-publish") {
			return true
		}
	}
	return false
}
