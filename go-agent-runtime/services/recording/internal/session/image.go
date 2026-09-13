package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

const (
	sightResultVersion     = 2
	sightResultSuccess     = "success"
	sightSourceScreen      = "screen"
	sightSourceBrowserPage = "browser_page"
	sightProjectionImage   = "input_image"
)

type sightResult struct {
	Version         int    `json:"version"`
	Status          string `json:"status"`
	Source          string `json:"source"`
	BrowserID       string `json:"browser_id,omitempty"`
	TargetID        string `json:"target_id,omitempty"`
	MIMEType        string `json:"mime_type,omitempty"`
	ByteLength      int    `json:"byte_length,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	TypedProjection string `json:"typed_projection,omitempty"`
}

type imageEvidence struct {
	callID          string
	Path            string
	Source          string
	BrowserID       string
	TargetID        string
	MIMEType        string
	ByteLength      int
	Width           int
	Height          int
	SHA256          string
	TypedProjection string
}

func (e imageEvidence) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Path            string `json:"path"`
		Source          string `json:"source"`
		BrowserID       string `json:"browser_id,omitempty"`
		TargetID        string `json:"target_id,omitempty"`
		MIMEType        string `json:"mime_type"`
		ByteLength      int    `json:"byte_length"`
		Width           int    `json:"width"`
		Height          int    `json:"height"`
		SHA256          string `json:"sha256"`
		TypedProjection string `json:"typed_projection"`
	}{e.Path, e.Source, e.BrowserID, e.TargetID, e.MIMEType, e.ByteLength, e.Width, e.Height, e.SHA256, e.TypedProjection})
}

func decodeImageCapture(call messages.ToolCall, response messages.ToolCallResponse, prefix string) (*imageEvidence, *transcript.RecordingArtifact, error) {
	result, ok := decodeSightResult(response.Content)
	if !ok {
		return nil, nil, nil
	}
	if err := validateSightTarget(call, result); err != nil {
		return nil, nil, err
	}
	part, err := singleImagePart(call, response)
	if err != nil {
		return nil, nil, err
	}
	result.MIMEType = strings.ToLower(strings.TrimSpace(result.MIMEType))
	if err := validateImagePart(part, result); err != nil {
		return nil, nil, err
	}
	path := fmt.Sprintf("%s-%s.%s", prefix, result.SHA256[:12], captureExtension(result.MIMEType))
	artifact := transcript.RecordingArtifact{Path: path, Data: append([]byte(nil), part.Bytes...), SHA256: result.SHA256}
	evidence := &imageEvidence{
		callID: call.ID, Path: path, Source: result.Source,
		BrowserID: result.BrowserID, TargetID: result.TargetID,
		MIMEType: result.MIMEType, ByteLength: result.ByteLength,
		Width: result.Width, Height: result.Height, SHA256: result.SHA256,
		TypedProjection: result.TypedProjection,
	}
	return evidence, &artifact, nil
}

func validateSightTarget(call messages.ToolCall, result sightResult) error {
	if result.Source != sightSourceBrowserPage {
		return nil
	}
	if strings.TrimSpace(result.BrowserID) == "" || strings.TrimSpace(result.TargetID) == "" {
		return fmt.Errorf("%s capture omitted selected target identity", call.Name)
	}
	return nil
}

func singleImagePart(call messages.ToolCall, response messages.ToolCallResponse) (messages.ImagePart, error) {
	parts := make([]messages.ImagePart, 0, 1)
	for _, part := range response.ContentParts {
		switch value := part.(type) {
		case messages.ImagePart:
			parts = append(parts, value)
		case *messages.ImagePart:
			if value != nil {
				parts = append(parts, *value)
			}
		}
	}
	if len(parts) != 1 {
		return messages.ImagePart{}, fmt.Errorf("%s capture returned %d image parts, want exactly one", call.Name, len(parts))
	}
	if len(parts[0].Bytes) == 0 {
		return messages.ImagePart{}, fmt.Errorf("%s capture image part is empty", call.Name)
	}
	return parts[0], nil
}

func validateImagePart(part messages.ImagePart, result sightResult) error {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(part.MediaType))
	if err != nil || strings.ToLower(strings.TrimSpace(mediaType)) != result.MIMEType {
		return fmt.Errorf("capture mime type %q does not match metadata %q", part.MediaType, result.MIMEType)
	}
	if !imageMetadataMatches(part.Bytes, result) {
		return errorsImage("capture image bytes do not match metadata digest or length")
	}
	decoded, _, err := image.Decode(bytes.NewReader(part.Bytes))
	if err != nil {
		return fmt.Errorf("capture image validation failed: %w", err)
	}
	if decoded.Bounds().Dx() != result.Width || decoded.Bounds().Dy() != result.Height {
		return fmt.Errorf("capture image dimensions are %dx%d, want %dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy(), result.Width, result.Height)
	}
	return nil
}

func imageMetadataMatches(data []byte, result sightResult) bool {
	digest := sha256.Sum256(data)
	digestText := hex.EncodeToString(digest[:])
	return result.ByteLength > 0 && result.ByteLength == len(data) && result.Width > 0 && result.Height > 0 &&
		len(result.SHA256) == sha256.Size*2 && strings.ToLower(result.SHA256) == result.SHA256 &&
		result.TypedProjection == sightProjectionImage && digestText == result.SHA256
}

func errorsImage(message string) error { return errors.New(message) }

func decodeSightResult(content string) (sightResult, bool) {
	var result sightResult
	if decodeStrictJSON([]byte(content), &result) == nil && validSightResult(result) {
		return result, true
	}
	var envelope struct {
		Version string          `json:"version"`
		OK      bool            `json:"ok"`
		Data    json.RawMessage `json:"data"`
	}
	if decodeStrictJSON([]byte(content), &envelope) != nil || envelope.Version != "webmcp.tool-result.v1" || !envelope.OK || len(envelope.Data) == 0 {
		return sightResult{}, false
	}
	if decodeStrictJSON(envelope.Data, &result) != nil || !validSightResult(result) {
		return sightResult{}, false
	}
	return result, true
}

func validSightResult(result sightResult) bool {
	if result.Version != sightResultVersion || result.Status != sightResultSuccess || (result.Source != sightSourceScreen && result.Source != sightSourceBrowserPage) {
		return false
	}
	if result.MIMEType == "" || result.ByteLength <= 0 || result.Width <= 0 || result.Height <= 0 || result.TypedProjection != sightProjectionImage {
		return false
	}
	if len(result.SHA256) != sha256.Size*2 || strings.ToLower(result.SHA256) != result.SHA256 {
		return false
	}
	_, err := hex.DecodeString(result.SHA256)
	return err == nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func captureExtension(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	default:
		return "img"
	}
}
