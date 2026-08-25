package sessionfixturevalidator

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

var committedSessionFixtureRoots = committedFixtureRoots()

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

func TestAllCommittedSessionFixturesPassWithExactCount(t *testing.T) {
	result, err := ValidatePaths(allCommittedFixtureRoots())
	if err != nil {
		t.Fatalf("validate all committed session fixture roots: %v", err)
	}
	if result.FilesScanned != 25 {
		t.Fatalf("ValidatePaths scanned %d committed session fixtures, want exact count 25", result.FilesScanned)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("all committed session fixture validation failed:\n%s", formatValidationErrors(result.Errors))
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

func TestCommittedSessionFixtureRootsStayWithinGatewayOwnedBoundaries(t *testing.T) {
	for _, root := range committedSessionFixtureRoots {
		normalized := filepath.ToSlash(root)
		if strings.Contains(normalized, "/agent-cli/") || strings.Contains(normalized, "agent-cli/test/integration/testdata") {
			t.Fatalf("committed session fixture root %q must not reach into agent-cli private testdata", normalized)
		}
	}

	sharedRoot := filepath.ToSlash(filepath.Dir(gatewaytesting.SharedSessionFixturePath("fixture.session.json")))
	if sharedRoot != filepath.ToSlash(committedSessionFixtureRoots[1]) {
		t.Fatalf("shared committed fixture root = %q, want %q", committedSessionFixtureRoots[1], sharedRoot)
	}
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

func committedFixtureRoots() []string {
	return []string{
		repoPathFromHere("../../pkg/providers/openai/testdata"),
		filepath.Dir(gatewaytesting.SharedSessionFixturePath("fixture.session.json")),
	}
}

func allCommittedFixtureRoots() []string {
	roots := committedFixtureRoots()
	return append(roots, repoPathFromHere("../../../agent-cli/test/integration/testdata"))
}

func repoPathFromHere(rel string) string {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return rel
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), rel))
}
