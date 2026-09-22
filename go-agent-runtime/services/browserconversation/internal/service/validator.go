package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation/internal/service/policy"
)

const maxBrowserConversationValidatorOutput = 1 << 20

type commandValidator struct {
	Command []string
	Dir     string
	Env     []string
	Timeout time.Duration
}

func NewCommandValidator(command []string, timeout time.Duration) (browserconversation.BrowserConversationValidator, error) {
	if err := validateBrowserConversationValidatorCommand(command, timeout); err != nil {
		return nil, err
	}
	return &commandValidator{Command: append([]string(nil), command...), Timeout: timeout}, nil
}

func (validator *commandValidator) ValidateBrowserConversation(result browserconversation.BrowserConversationResult) (browserconversation.BrowserConversationValidatorVerdict, error) {
	if validator == nil {
		return browserconversation.BrowserConversationValidatorVerdict{}, errors.New("browser conversation command validator is nil")
	}
	if err := validateBrowserConversationValidatorBoundary(validator.Command, validator.Dir, validator.Env, validator.Timeout); err != nil {
		return browserconversation.BrowserConversationValidatorVerdict{}, err
	}
	input, err := policy.NewValidatorInput(result)
	if err != nil {
		return browserconversation.BrowserConversationValidatorVerdict{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return browserconversation.BrowserConversationValidatorVerdict{}, errors.New("encode validator input")
	}
	process := runBrowserConversationValidator(context.Background(), validator.Command, validator.Dir, browserConversationValidatorEnvironment(validator.Env), payload, validator.Timeout)
	if process.err != nil {
		return browserconversation.BrowserConversationValidatorVerdict{}, process.err
	}
	if process.stdoutTruncated || process.stderrTruncated {
		return browserconversation.BrowserConversationValidatorVerdict{}, browserconversation.ErrBrowserConversationValidatorOutput
	}
	verdict, err := decodeBrowserConversationValidatorVerdict(process.stdout)
	if err != nil {
		return browserconversation.BrowserConversationValidatorVerdict{}, errors.Join(browserconversation.ErrBrowserConversationValidatorVerdict, err)
	}
	if err := policy.ValidateValidatorVerdict(verdict); err != nil {
		return browserconversation.BrowserConversationValidatorVerdict{}, errors.Join(browserconversation.ErrBrowserConversationValidatorVerdict, err)
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
	return policy.ContainsCredentialMarker(value)
}

func validateBrowserConversationValidatorCommand(command []string, timeout time.Duration) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return errors.Join(browserconversation.ErrBrowserConversationValidatorCommand, errors.New("validator command is required"))
	}
	if timeout <= 0 {
		return errors.Join(browserconversation.ErrBrowserConversationValidatorCommand, errors.New("validator command timeout must be positive"))
	}
	for _, part := range command {
		if policy.ContainsCredentialMarker(part) {
			return errors.Join(browserconversation.ErrBrowserConversationValidatorCommand, errors.New("validator command must not contain credential-shaped arguments"))
		}
	}
	return nil
}

func validateBrowserConversationValidatorBoundary(command []string, dir string, env []string, timeout time.Duration) error {
	if err := validateBrowserConversationValidatorCommand(command, timeout); err != nil {
		return err
	}
	if policy.ContainsCredentialMarker(dir) {
		return errors.Join(browserconversation.ErrBrowserConversationValidatorCommand, errors.New("validator working directory must not contain credential-shaped text"))
	}
	for _, value := range env {
		if policy.ContainsCredentialMarker(value) {
			return errors.Join(browserconversation.ErrBrowserConversationValidatorCommand, errors.New("validator environment must not contain credential-shaped text"))
		}
	}
	return nil
}

func decodeBrowserConversationValidatorVerdict(payload []byte) (browserconversation.BrowserConversationValidatorVerdict, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var verdict browserconversation.BrowserConversationValidatorVerdict
	if err := decoder.Decode(&verdict); err != nil {
		return verdict, errors.New("decode validator verdict")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return browserconversation.BrowserConversationValidatorVerdict{}, errors.New("validator verdict must contain one JSON object")
	}
	return verdict, nil
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
