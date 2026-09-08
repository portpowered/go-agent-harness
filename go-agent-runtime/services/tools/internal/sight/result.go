// Package sight contains the small, provider-neutral contract shared by
// screen and browser-page image captures.
package sight

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"strings"
)

const (
	ResultVersion             = 2
	StatusSuccess             = "success"
	StatusError               = "error"
	TypedProjectionInputImage = "input_image"
	SourceScreen              = "screen"
	SourceBrowserPage         = "browser_page"
	webMCPResultVersion       = "webmcp.tool-result.v1"
)

// Result is the bounded textual projection of one immutable image capture.
// The pixels are carried separately by a messages.ImagePart; this value never
// contains a data URL or base64 payload.
type Result struct {
	Version         int     `json:"version"`
	Status          string  `json:"status"`
	Source          string  `json:"source"`
	BrowserID       string  `json:"browser_id,omitempty"`
	TargetID        string  `json:"target_id,omitempty"`
	MIMEType        string  `json:"mime_type,omitempty"`
	ByteLength      int     `json:"byte_length,omitempty"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	FrameCount      int     `json:"frame_count,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	SHA256          string  `json:"sha256,omitempty"`
	TypedProjection string  `json:"typed_projection,omitempty"`
	ErrorCode       string  `json:"error_code,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// NewSuccess builds the metadata for bytes that have already been captured
// and decoded. It hashes the exact byte slice that is expected to be sent as
// the typed projection.
func NewSuccess(source, mediaType string, pixels []byte, width, height int) (Result, error) {
	mediaType, err := normalizedMIME(mediaType)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(source) == "" {
		return Result{}, fmt.Errorf("sight result source is required")
	}
	if len(pixels) == 0 {
		return Result{}, fmt.Errorf("sight result pixels are empty")
	}
	if width <= 0 || height <= 0 {
		return Result{}, fmt.Errorf("sight result dimensions must be positive")
	}
	digest := sha256.Sum256(pixels)
	result := Result{
		Version:         ResultVersion,
		Status:          StatusSuccess,
		Source:          strings.TrimSpace(source),
		MIMEType:        mediaType,
		ByteLength:      len(pixels),
		Width:           width,
		Height:          height,
		SHA256:          hex.EncodeToString(digest[:]),
		TypedProjection: TypedProjectionInputImage,
	}
	return result, nil
}

// NewError builds a non-pixel result for a capture denial or failure.
func NewError(source string, cause error) Result {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "unknown"
	}
	message := "image capture failed"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message = strings.TrimSpace(cause.Error())
	}
	return Result{Version: ResultVersion, Status: StatusError, Source: source, ErrorCode: "capture_failed", Error: message}
}

// Encode validates and emits one compact JSON result.
func Encode(result Result) ([]byte, error) {
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// Decode accepts either a direct sight result or the same result nested in a
// successful WebMCP result envelope. The latter keeps the browser tool's
// existing classified error contract intact while allowing its successful
// capture to carry a typed image projection.
func Decode(data []byte) (Result, error) {
	var direct Result
	if err := decodeStrict(data, &direct); err == nil {
		if validateErr := direct.Validate(); validateErr == nil {
			return direct, nil
		}
	}

	var envelope struct {
		Version string          `json:"version"`
		OK      bool            `json:"ok"`
		Data    json.RawMessage `json:"data"`
		Error   json.RawMessage `json:"error"`
	}
	if err := decodeStrict(data, &envelope); err != nil {
		return Result{}, fmt.Errorf("decode sight result: %w", err)
	}
	if envelope.Version != webMCPResultVersion || !envelope.OK || len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return Result{}, fmt.Errorf("decode sight result: unsupported or failed envelope")
	}
	var nested Result
	if err := decodeStrict(envelope.Data, &nested); err != nil {
		return Result{}, fmt.Errorf("decode sight result data: %w", err)
	}
	if err := nested.Validate(); err != nil {
		return Result{}, err
	}
	return nested, nil
}

func (r Result) Validate() error {
	if err := r.validateIdentity(); err != nil {
		return err
	}
	switch r.Status {
	case StatusSuccess:
		return r.validateSuccess()
	case StatusError:
		return r.validateFailure()
	default:
		return fmt.Errorf("sight result status %q is unsupported", r.Status)
	}
}

func (r Result) validateIdentity() error {
	if r.Version != ResultVersion {
		return fmt.Errorf("sight result version %d is unsupported", r.Version)
	}
	if strings.TrimSpace(r.Source) == "" {
		return fmt.Errorf("sight result source is required")
	}
	return nil
}

func (r Result) validateSuccess() error {
	mediaType, err := normalizedMIME(r.MIMEType)
	if err != nil {
		return err
	}
	if err := r.validateSuccessMetadata(mediaType); err != nil {
		return err
	}
	if err := r.validateAnimationMetadata(); err != nil {
		return err
	}
	if !isSHA256(r.SHA256) {
		return fmt.Errorf("sight result sha256 is invalid")
	}
	if r.TypedProjection != TypedProjectionInputImage {
		return fmt.Errorf("sight result typed projection is invalid")
	}
	if r.Error != "" {
		return fmt.Errorf("successful sight result cannot contain an error")
	}
	return nil
}

func (r Result) validateSuccessMetadata(mediaType string) error {
	if mediaType != r.MIMEType || r.ByteLength <= 0 || r.Width <= 0 || r.Height <= 0 {
		return fmt.Errorf("sight result success metadata is incomplete")
	}
	return nil
}

func (r Result) validateAnimationMetadata() error {
	if r.FrameCount < 0 || r.DurationSeconds < 0 || (r.FrameCount > 0 && r.DurationSeconds <= 0) {
		return fmt.Errorf("sight result animation metadata is invalid")
	}
	return nil
}

func (r Result) validateFailure() error {
	if strings.TrimSpace(r.ErrorCode) == "" || strings.TrimSpace(r.Error) == "" {
		return fmt.Errorf("sight result error classification and message are required")
	}
	if r.hasImageMetadata() {
		return fmt.Errorf("failed sight result must not contain image metadata")
	}
	return nil
}

func (r Result) hasImageMetadata() bool {
	return r.MIMEType != "" || r.ByteLength != 0 || r.Width != 0 || r.Height != 0 ||
		r.FrameCount != 0 || r.DurationSeconds != 0 || r.SHA256 != "" || r.TypedProjection != ""
}

func normalizedMIME(raw string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(raw))
	if err != nil || strings.TrimSpace(mediaType) == "" {
		if err == nil {
			err = fmt.Errorf("media type is empty")
		}
		return "", fmt.Errorf("sight result mime type is invalid: %w", err)
	}
	return strings.ToLower(strings.TrimSpace(mediaType)), nil
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
