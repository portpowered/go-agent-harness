package sessionfixturevalidator

import (
	"os"
	"path/filepath"
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

func TestCommittedSessionFixturesCoverReplayableRepositoryFixtures(t *testing.T) {
	repositoryFixtures, err := collectSessionFixtureFiles([]string{"../../.."})
	if err != nil {
		t.Fatalf("collect repository session fixtures: %v", err)
	}
	committedFixtures, err := collectSessionFixtureFiles(committedSessionFixtureRoots)
	if err != nil {
		t.Fatalf("collect committed session fixture roots: %v", err)
	}

	expected := make(map[string]struct{}, len(committedFixtures))
	for _, fixture := range committedFixtures {
		absolutePath, err := filepath.Abs(fixture)
		if err != nil {
			t.Fatalf("abs committed fixture path %s: %v", fixture, err)
		}
		expected[filepath.Clean(absolutePath)] = struct{}{}
	}

	var unexpected []string
	for _, fixture := range repositoryFixtures {
		absolutePath, err := filepath.Abs(fixture)
		if err != nil {
			t.Fatalf("abs repository fixture path %s: %v", fixture, err)
		}
		cleaned := filepath.Clean(absolutePath)
		if isValidatorNegativeFixture(cleaned) {
			continue
		}
		if _, ok := expected[cleaned]; !ok {
			unexpected = append(unexpected, cleaned)
		}
	}
	if len(unexpected) != 0 {
		t.Fatalf("repository contains replayable session fixtures outside committed validation roots:\n%s", strings.Join(unexpected, "\n"))
	}
}

func TestCommittedSessionFixturesLoadThroughExpectedReplaySurface(t *testing.T) {
	fixtures, err := collectSessionFixtureFiles(committedSessionFixtureRoots)
	if err != nil {
		t.Fatalf("collect committed session fixtures: %v", err)
	}

	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			capture, err := gatewaytesting.LoadSessionCapture(fixture)
			if err != nil {
				t.Fatalf("load fixture %s: %v", fixture, err)
			}

			hasStreamPayload := false
			hasWebSocketPayload := false
			for _, record := range capture.Records {
				switch record.PayloadType {
				case gatewaytesting.SessionPayloadTypeStreamMessage:
					hasStreamPayload = true
				case gatewaytesting.SessionPayloadTypeWebSocketMessage:
					hasWebSocketPayload = true
				}
			}

			switch {
			case hasStreamPayload && hasWebSocketPayload:
				t.Fatalf("fixture mixes stream_message and websocket_message payloads: %s", fixture)
			case hasWebSocketPayload:
				dialer, err := gatewaytesting.NewReplayWebSocketDialer(fixture)
				if err != nil {
					t.Fatalf("load websocket replay fixture %s: %v", fixture, err)
				}
				if dialer.Model() == "" {
					t.Fatalf("websocket replay fixture %s did not retain provider model metadata", fixture)
				}
			default:
				replayer, err := gatewaytesting.NewSessionReplayer(fixture, gatewaytesting.WithReplayOutboundValidation(false))
				if err != nil {
					t.Fatalf("load stream replay fixture %s: %v", fixture, err)
				}
				_ = replayer.Close()
			}
		})
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

func isValidatorNegativeFixture(path string) bool {
	needle := filepath.Join(
		"go-llm-gateway",
		"internal",
		"sessionfixturevalidator",
		"testdata",
		"invalid-session-fixtures",
	) + string(os.PathSeparator)
	return strings.Contains(path, needle)
}
