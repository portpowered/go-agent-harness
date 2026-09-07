package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// FilesystemRefusalType and FilesystemRefusalVersion identify the stable
	// provider-neutral result returned when filesystem policy rejects a call.
	FilesystemRefusalType    = "filesystem_refusal"
	FilesystemRefusalVersion = "filesystem.refusal.v1"
	FilesystemRefusalStatus  = "refused"
)

// FilesystemRefusalReason is the small, stable reason vocabulary exposed to
// customers and models. The underlying filesystem error is intentionally not
// serialized because it may contain platform-specific or sensitive details.
type FilesystemRefusalReason string

const (
	FilesystemRefusalOutsidePermittedRoots FilesystemRefusalReason = "outside_permitted_roots"
	FilesystemRefusalSensitiveRead         FilesystemRefusalReason = "sensitive_read"
	FilesystemRefusalInvalidScope          FilesystemRefusalReason = "invalid_scope"
)

var ErrFilesystemRefused = errors.New("filesystem operation refused")

// FilesystemRefusal is the structured result visible to a model and to direct
// CLI callers. Path is the requested tool path for ordinary refusals; sensitive
// targets are represented by a safe marker so a denial cannot disclose a
// protected pathname or its contents.
type FilesystemRefusal struct {
	Type        string                  `json:"type"`
	Version     string                  `json:"version"`
	OK          bool                    `json:"ok"`
	Status      string                  `json:"status"`
	Operation   string                  `json:"operation"`
	Path        string                  `json:"path"`
	WorkDir     string                  `json:"workdir"`
	Reason      FilesystemRefusalReason `json:"reason"`
	Message     string                  `json:"message"`
	Remediation string                  `json:"remediation"`
}

func (r FilesystemRefusal) Error() string {
	if r.Operation == "" {
		return ErrFilesystemRefused.Error()
	}
	return fmt.Sprintf("filesystem operation %q refused: %s", r.Operation, r.Reason)
}

// FilesystemRefusalError carries the same refusal identity through Go error
// paths such as the direct CLI while keeping the serialized envelope stable.
type FilesystemRefusalError struct {
	Refusal FilesystemRefusal
}

func (e *FilesystemRefusalError) Error() string {
	if e == nil {
		return ErrFilesystemRefused.Error()
	}
	if e.Refusal.Operation == "" {
		return ErrFilesystemRefused.Error()
	}
	return e.Refusal.Error()
}

func (e *FilesystemRefusalError) Unwrap() error { return ErrFilesystemRefused }

// Validate checks the refusal contract without inspecting or resolving the
// requested path. Callers can therefore validate a model-visible result
// without reintroducing filesystem access.
func (r FilesystemRefusal) Validate() error {
	if r.Type != FilesystemRefusalType {
		return fmt.Errorf("unsupported filesystem refusal type %q", r.Type)
	}
	if r.Version != FilesystemRefusalVersion {
		return fmt.Errorf("unsupported filesystem refusal version %q", r.Version)
	}
	if r.OK || r.Status != FilesystemRefusalStatus {
		return fmt.Errorf("filesystem refusal must have ok=false and status=%q", FilesystemRefusalStatus)
	}
	if strings.TrimSpace(r.Operation) == "" || strings.TrimSpace(r.Path) == "" || strings.TrimSpace(r.WorkDir) == "" {
		return fmt.Errorf("filesystem refusal operation, path, and workdir are required")
	}
	switch r.Reason {
	case FilesystemRefusalOutsidePermittedRoots, FilesystemRefusalSensitiveRead, FilesystemRefusalInvalidScope:
	default:
		return fmt.Errorf("unsupported filesystem refusal reason %q", r.Reason)
	}
	if strings.TrimSpace(r.Message) == "" || strings.TrimSpace(r.Remediation) == "" {
		return fmt.Errorf("filesystem refusal message and remediation are required")
	}
	return nil
}

// MarshalFilesystemRefusal serializes one validated refusal envelope.
func MarshalFilesystemRefusal(refusal FilesystemRefusal) ([]byte, error) {
	if err := refusal.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(refusal)
}

// DecodeFilesystemRefusal decodes and validates one refusal envelope.
func DecodeFilesystemRefusal(data []byte) (FilesystemRefusal, error) {
	var refusal FilesystemRefusal
	if err := json.Unmarshal(data, &refusal); err != nil {
		return FilesystemRefusal{}, err
	}
	if err := refusal.Validate(); err != nil {
		return FilesystemRefusal{}, err
	}
	return refusal, nil
}

// FilesystemRefusalFromContent recognizes both the direct refusal shape and
// the nested refusal carried by the read_image result envelope.
func FilesystemRefusalFromContent(content string) (FilesystemRefusal, bool) {
	if refusal, err := DecodeFilesystemRefusal([]byte(content)); err == nil {
		return refusal, true
	}

	var wrapped struct {
		Refusal *FilesystemRefusal `json:"refusal"`
	}
	if err := json.Unmarshal([]byte(content), &wrapped); err != nil || wrapped.Refusal == nil {
		return FilesystemRefusal{}, false
	}
	if err := wrapped.Refusal.Validate(); err != nil {
		return FilesystemRefusal{}, false
	}
	return *wrapped.Refusal, true
}
