package sessionfixturevalidator

import (
	"strings"
	"testing"

	gatewaytesting "github.com/portpowered/go-llm-gateway/pkg/testing"
)

var committedSessionFixtureRoots = []string{
	"../../pkg/providers/openai/testdata",
	"../../pkg/testing/testdata/session-fixtures",
	"../../../agent-cli/test/integration/testdata",
}

func TestCommittedSessionFixturesPassHygieneSmokeCheck(t *testing.T) {
	result, err := ValidatePaths(committedSessionFixtureRoots)
	if err != nil {
		t.Fatalf("validate committed session fixture roots: %v", err)
	}
	if result.FilesScanned == 0 {
		t.Fatalf("ValidatePaths scanned 0 committed session fixtures from %v", committedSessionFixtureRoots)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("committed session fixture hygiene failed:\n%s", formatValidationErrors(result.Errors))
	}
}

func TestCommittedSessionFixturesSmokeCheckReportsInvalidFixtureHygiene(t *testing.T) {
	result, err := ValidatePaths([]string{"testdata/invalid-session-fixtures"})
	if err != nil {
		t.Fatalf("validate invalid session fixture testdata: %v", err)
	}

	requireSmokeValidationError(t, result, "missing-provenance.session.json", "session.fixture_provenance", "must be present")
	requireSmokeValidationError(t, result, "unsafe-synthetic.session.json", "records[0].payload.value.input_audio", "raw audio")
	requireSmokeValidationError(t, result, "unsafe-synthetic.session.json", "records[0].payload.value.authorization", "credential-like")
	requireSmokeValidationError(t, result, "provider-wire-misuse.session.json", "records[0].payload_type", "websocket_message")
}

func requireSmokeValidationError(t *testing.T, result Result, fileName, fieldPath, reason string) {
	t.Helper()

	for _, validationErr := range result.Errors {
		if strings.Contains(validationErr.File, fileName) &&
			validationErr.FieldPath == fieldPath &&
			strings.Contains(validationErr.Reason, reason) {
			return
		}
	}
	t.Fatalf("missing validation error for file containing %q, field %q, reason containing %q; got:\n%s",
		fileName,
		fieldPath,
		reason,
		formatValidationErrors(result.Errors),
	)
}

func formatValidationErrors(errs []gatewaytesting.SessionFixtureValidationError) string {
	if len(errs) == 0 {
		return "(none)"
	}
	var lines []string
	for _, err := range errs {
		lines = append(lines, err.Error())
	}
	return strings.Join(lines, "\n")
}
