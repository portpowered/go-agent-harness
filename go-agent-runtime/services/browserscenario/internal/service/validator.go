package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario"
)

const maxBrowserConversationValidatorOutput = 1 << 20

type commandValidator struct {
	Command []string
	Dir     string
	Env     []string
	Timeout time.Duration
}

func NewCommandValidator(command []string, timeout time.Duration) (browserscenario.BrowserConversationValidator, error) {
	if err := validateBrowserConversationValidatorCommand(command, timeout); err != nil {
		return nil, err
	}
	return &commandValidator{Command: append([]string(nil), command...), Timeout: timeout}, nil
}

func (validator *commandValidator) ValidateBrowserConversation(result browserscenario.BrowserConversationResult) (browserscenario.BrowserConversationValidatorVerdict, error) {
	if validator == nil {
		return browserscenario.BrowserConversationValidatorVerdict{}, errors.New("browser conversation command validator is nil")
	}
	if err := validateBrowserConversationValidatorBoundary(validator.Command, validator.Dir, validator.Env, validator.Timeout); err != nil {
		return browserscenario.BrowserConversationValidatorVerdict{}, err
	}
	input, err := result.ValidatorInput()
	if err != nil {
		return browserscenario.BrowserConversationValidatorVerdict{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return browserscenario.BrowserConversationValidatorVerdict{}, errors.New("encode validator input")
	}
	process := runBrowserConversationValidator(context.Background(), validator.Command, validator.Dir, browserConversationValidatorEnvironment(validator.Env), payload, validator.Timeout)
	if process.err != nil {
		return browserscenario.BrowserConversationValidatorVerdict{}, process.err
	}
	if process.stdoutTruncated || process.stderrTruncated {
		return browserscenario.BrowserConversationValidatorVerdict{}, browserscenario.ErrBrowserConversationValidatorOutput
	}
	verdict, err := decodeBrowserConversationValidatorVerdict(process.stdout)
	if err != nil {
		return browserscenario.BrowserConversationValidatorVerdict{}, errors.Join(browserscenario.ErrBrowserConversationValidatorVerdict, err)
	}
	if err := validateBrowserConversationValidatorVerdict(verdict); err != nil {
		return browserscenario.BrowserConversationValidatorVerdict{}, errors.Join(browserscenario.ErrBrowserConversationValidatorVerdict, err)
	}
	return verdict, nil
}

func browserConversationValidatorEnvironment(env []string) []string {
	if env != nil {
		return append([]string(nil), env...)
	}
	safe := make([]string, 0)
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || browserConversationUnsafeEnvironment(name, value) {
			continue
		}
		safe = append(safe, entry)
	}
	return safe
}

func browserConversationUnsafeEnvironment(name, value string) bool {
	upperName := strings.ToUpper(name)
	for _, marker := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC"} {
		if strings.Contains(upperName, marker) {
			return true
		}
	}
	return browserConversationContainsCredentialMarker(value)
}

func validateBrowserConversationValidatorCommand(command []string, timeout time.Duration) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return errors.Join(browserscenario.ErrBrowserConversationValidatorCommand, errors.New("validator command is required"))
	}
	if timeout <= 0 {
		return errors.Join(browserscenario.ErrBrowserConversationValidatorCommand, errors.New("validator command timeout must be positive"))
	}
	for _, part := range command {
		if browserConversationContainsCredentialMarker(part) {
			return errors.Join(browserscenario.ErrBrowserConversationValidatorCommand, errors.New("validator command must not contain credential-shaped arguments"))
		}
	}
	return nil
}

func validateBrowserConversationValidatorBoundary(command []string, dir string, env []string, timeout time.Duration) error {
	if err := validateBrowserConversationValidatorCommand(command, timeout); err != nil {
		return err
	}
	if browserConversationContainsCredentialMarker(dir) {
		return errors.Join(browserscenario.ErrBrowserConversationValidatorCommand, errors.New("validator working directory must not contain credential-shaped text"))
	}
	for _, value := range env {
		if browserConversationContainsCredentialMarker(value) {
			return errors.Join(browserscenario.ErrBrowserConversationValidatorCommand, errors.New("validator environment must not contain credential-shaped text"))
		}
	}
	return nil
}

func decodeBrowserConversationValidatorVerdict(payload []byte) (browserscenario.BrowserConversationValidatorVerdict, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var verdict browserscenario.BrowserConversationValidatorVerdict
	if err := decoder.Decode(&verdict); err != nil {
		return verdict, errors.New("decode validator verdict")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return browserscenario.BrowserConversationValidatorVerdict{}, errors.New("validator verdict must contain one JSON object")
	}
	return verdict, nil
}

func validateBrowserConversationValidatorVerdict(verdict browserscenario.BrowserConversationValidatorVerdict) error {
	if verdict.Version != "" && verdict.Version != browserscenario.BrowserConversationValidatorVersion {
		return fmt.Errorf("validator verdict version must be %q", browserscenario.BrowserConversationValidatorVersion)
	}
	if verdict.Status == "" {
		return errors.New("validator verdict status is required")
	}
	if !validBrowserConversationValidatorStatus(verdict.Status) {
		return errors.New("validator verdict status is unsupported")
	}
	if verdict.Status == browserscenario.BrowserConversationValidatorPass && !verdict.Passed {
		return errors.New("validator pass status contradicted passed=false")
	}
	if verdict.Status == browserscenario.BrowserConversationValidatorFail && verdict.Passed {
		return errors.New("validator fail status contradicted passed=true")
	}
	if verdict.Status == browserscenario.BrowserConversationValidatorNotRun {
		return nil
	}
	return validateBrowserConversationChecks(verdict.Checks)
}

func validBrowserConversationValidatorStatus(status browserscenario.BrowserConversationValidatorStatus) bool {
	return status == browserscenario.BrowserConversationValidatorPass || status == browserscenario.BrowserConversationValidatorFail || status == browserscenario.BrowserConversationValidatorNotRun
}

func validateBrowserConversationChecks(checks []browserscenario.BrowserConversationValidatorCheck) error {
	wanted := make(map[string]struct{}, len((browserscenario.BrowserConversationValidatorRubric{}).Values()))
	for _, name := range (browserscenario.BrowserConversationValidatorRubric{}).Values() {
		wanted[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if _, ok := wanted[check.Name]; !ok {
			return fmt.Errorf("validator verdict contains unsupported check %q", check.Name)
		}
		if _, duplicate := seen[check.Name]; duplicate {
			return fmt.Errorf("validator verdict repeats check %q", check.Name)
		}
		seen[check.Name] = struct{}{}
	}
	if len(seen) != len(wanted) {
		return errors.New("validator verdict did not cover the fixed rubric")
	}
	return nil
}

type browserConversationBoundedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (buffer *browserConversationBoundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - len(buffer.data)
	if remaining <= 0 {
		buffer.truncated = true
		return len(value), nil
	}
	if len(value) > remaining {
		buffer.data = append(buffer.data, value[:remaining]...)
		buffer.truncated = true
		return len(value), nil
	}
	buffer.data = append(buffer.data, value...)
	return len(value), nil
}

func (buffer *browserConversationBoundedBuffer) Bytes() []byte  { return buffer.data }
func (buffer *browserConversationBoundedBuffer) Len() int       { return len(buffer.data) }
func (buffer *browserConversationBoundedBuffer) String() string { return string(buffer.data) }

func browserConversationContainsCredentialMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"authorization:", "bearer ", "api_key", "api-key", "access_token", "refresh_token", "client_secret", "password", "-----begin ", "sk-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
