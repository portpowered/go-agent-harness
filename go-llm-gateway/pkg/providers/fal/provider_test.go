package fal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// mockTransport captures the last request and returns a configured response.
type mockTransport struct {
	statusCode int
	body       string
	lastReq    *http.Request
	lastBody   []byte
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.lastReq = req
	var err error
	if m.lastBody, err = replayableRequestBody(req); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: m.statusCode,
		Body:       io.NopCloser(strings.NewReader(m.body)),
		Header:     make(http.Header),
	}, nil
}

func TestFalProvider_Infer_InvalidRequests(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{statusCode: 200, body: "{}"}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	tests := []struct {
		name      string
		req       providers.InferenceRequest
		wantErr   string
		wantClass error
		wantField string
	}{
		{
			name:      "missing model",
			req:       providers.InferenceRequest{Messages: []models.Message{models.NewTextMessage(models.RoleUser, "hi")}},
			wantErr:   "fal provider requires Model to be set",
			wantClass: providers.ErrInvalidRequest,
			wantField: "model",
		},
		{
			name: "unsupported model",
			req: providers.InferenceRequest{
				Model: "fal-ai/other/model",
				Messages: []models.Message{{
					Role: models.RoleUser,
					ContentParts: []models.ContentPart{
						models.TextPart{Text: "prompt"},
						models.AudioPart{URL: "https://example.com/audio.mp3"},
					},
				}},
			},
			wantErr:   "unsupported model",
			wantClass: providers.ErrUnsupportedRequest,
			wantField: "model",
		},
		{
			name:    "no user message",
			req:     providers.InferenceRequest{Model: ModelLTXAudioToVideo, Messages: []models.Message{models.NewTextMessage(models.RoleAssistant, "ok")}},
			wantErr: "no user message with audio or text found",
		},
		{name: "empty messages", req: providers.InferenceRequest{Model: ModelLTXAudioToVideo, Messages: []models.Message{}}, wantErr: "no user message with audio or text found"},
		{
			name: "LTX with text only (no audio)",
			req: providers.InferenceRequest{
				Model: ModelLTXAudioToVideo,
				Messages: []models.Message{{
					Role:         models.RoleUser,
					ContentParts: []models.ContentPart{models.TextPart{Text: "A woman speaks"}},
				}},
			},
			wantErr:   "audio_url is required",
			wantClass: providers.ErrInvalidRequest,
			wantField: "audio_url",
		},
		{
			name: "Qwen with text only (no audio)",
			req: providers.InferenceRequest{
				Model: ModelQwenCloneVoice,
				Messages: []models.Message{{
					Role:         models.RoleUser,
					ContentParts: []models.ContentPart{models.TextPart{Text: "reference"}},
				}},
			},
			wantErr:   "audio_url is required",
			wantClass: providers.ErrInvalidRequest,
			wantField: "audio_url",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.Infer(ctx, tt.req)
			assertFalInvalidRequestError(t, err, tt.wantErr, tt.wantClass, tt.wantField)
		})
	}
}

// assertFalInvalidRequestError checks that err carries wantErr, wraps
// wantClass when set, and is a fal ValidationError for wantField when set.
func assertFalInvalidRequestError(t *testing.T, err error, wantErr string, wantClass error, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Infer() expected error containing %q, got nil", wantErr)
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Errorf("Infer() error = %v, want substring %q", err, wantErr)
	}
	if wantClass != nil && !errors.Is(err, wantClass) {
		t.Fatalf("Infer() error = %v, want class %v", err, wantClass)
	}
	if wantField != "" {
		var validationErr *providers.ValidationError
		if !errors.As(err, &validationErr) {
			t.Fatalf("Infer() error = %T, want ValidationError", err)
		}
		if validationErr.Provider != "fal" || validationErr.Feature != wantField {
			t.Fatalf("ValidationError = %+v, want provider fal feature %q", validationErr, wantField)
		}
	}
}

func TestFalProvider_Infer_LTXAudioToVideo_ValidRequestAndResponse(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"video":{"url":"https://storage.example.com/out.mp4","content_type":"video/mp4","file_name":"out.mp4"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithAPIKey("test-key"), WithBaseURL("https://fal.run"), WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelLTXAudioToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.TextPart{Text: "A woman speaks to the camera"},
				models.AudioPart{URL: "https://example.com/speech.mp3"},
			},
		}},
	}

	resp, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}

	assertFalRequest(t, transport, "/fal-ai/ltx-2-19b/audio-to-video")
	var body ltxAudioToVideoRequest
	if err := json.Unmarshal(transport.lastBody, &body); err != nil {
		t.Fatalf("request body JSON: %v", err)
	}
	if body.Prompt != "A woman speaks to the camera" {
		t.Errorf("request prompt = %q, want %q", body.Prompt, "A woman speaks to the camera")
	}
	if body.AudioURL != "https://example.com/speech.mp3" {
		t.Errorf("request audio_url = %q, want https://example.com/speech.mp3", body.AudioURL)
	}

	assertFalVideoResponse(t, resp, "https://storage.example.com/out.mp4")
}

func TestFalProvider_Infer_LTXAudioToVideo_InlineAudioDataURI(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"video":{"url":"https://storage.example.com/out.mp4","content_type":"video/mp4"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelLTXAudioToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.TextPart{Text: "Prompt"},
				models.AudioPart{Bytes: []byte("wav-bytes"), MediaType: "audio/wav"},
			},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}
	var body ltxAudioToVideoRequest
	if err := json.Unmarshal(transport.lastBody, &body); err != nil {
		t.Fatalf("request body JSON: %v", err)
	}
	if !strings.HasPrefix(body.AudioURL, "data:audio/wav;base64,") {
		t.Errorf("request audio_url should be data URI, got %q", body.AudioURL)
	}
}

func TestFalProvider_Infer_QwenCloneVoice_ValidRequestAndResponse(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"speaker_embedding":{"url":"https://storage.example.com/embed.safetensors","content_type":"application/octet-stream","file_name":"embed.safetensors"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelQwenCloneVoice,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.AudioPart{URL: "https://example.com/voice.mp3"},
				models.TextPart{Text: "Optional reference text for the recording."},
			},
		}},
	}

	resp, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}

	// Validate outgoing request
	var body qwenCloneVoiceRequest
	if err := json.Unmarshal(transport.lastBody, &body); err != nil {
		t.Fatalf("request body JSON: %v", err)
	}
	if body.AudioURL != "https://example.com/voice.mp3" {
		t.Errorf("request audio_url = %q, want https://example.com/voice.mp3", body.AudioURL)
	}
	if body.ReferenceText != "Optional reference text for the recording." {
		t.Errorf("request reference_text = %q", body.ReferenceText)
	}

	// Validate response
	if len(resp.Message.ContentParts) != 2 {
		t.Fatalf("response ContentParts length = %d, want 2 (EmbeddingPart + URL TextPart)", len(resp.Message.ContentParts))
	}
	ep, ok := resp.Message.ContentParts[0].(models.EmbeddingPart)
	if !ok {
		t.Fatalf("response ContentParts[0] = %T, want models.EmbeddingPart", resp.Message.ContentParts[0])
	}
	if ep.URL != "https://storage.example.com/embed.safetensors" {
		t.Errorf("EmbeddingPart.URL = %q, want https://storage.example.com/embed.safetensors", ep.URL)
	}
	if resp.Message.TextContent() != "https://storage.example.com/embed.safetensors" {
		t.Errorf("Message.TextContent() = %q, want embedding URL", resp.Message.TextContent())
	}
}

func TestFalProvider_Infer_QwenCloneVoice_TextFromContent(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"speaker_embedding":{"url":"https://x/s.safetensors","content_type":"application/octet-stream"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	// Use Content (no ContentParts) for the last user message: Content becomes single text part;
	// we still need audio from another message or same message. Here we use ContentParts with audio + Content ignored when ContentParts set.
	// Actually when ContentParts is empty we use Content as single TextPart. So message with only Content has text only → audioURL "" → error.
	// Test "last user message" selection: two user messages, second has audio+text.
	req := providers.InferenceRequest{
		Model: ModelQwenCloneVoice,
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "first"),
			{
				Role: models.RoleUser,
				ContentParts: []models.ContentPart{
					models.AudioPart{URL: "https://example.com/a.mp3"},
				},
			},
		},
	}

	resp, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}
	var body qwenCloneVoiceRequest
	if err := json.Unmarshal(transport.lastBody, &body); err != nil {
		t.Fatalf("request body JSON: %v", err)
	}
	if body.AudioURL != "https://example.com/a.mp3" {
		t.Errorf("request audio_url = %q (expected last user message)", body.AudioURL)
	}
	if len(resp.Message.ContentParts) != 2 {
		t.Fatalf("response ContentParts length = %d, want 2 (EmbeddingPart + URL TextPart)", len(resp.Message.ContentParts))
	}
}

func TestFalProvider_Infer_HTTPError(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 400,
		body:       `{"detail":"invalid audio_url"}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelLTXAudioToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.TextPart{Text: "prompt"},
				models.AudioPart{URL: "https://example.com/audio.mp3"},
			},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err == nil {
		t.Fatal("Infer() expected error on 400, got nil")
	}
	if !errors.Is(err, providers.ErrProviderRejected) {
		t.Fatalf("Infer() error = %v, want ErrProviderRejected", err)
	}
	if !errors.Is(err, providers.ErrInvalidRequest) {
		t.Fatalf("Infer() error = %v, want ErrInvalidRequest", err)
	}
	var providerErr *providers.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("Infer() error = %T, want ProviderError", err)
	}
	if providerErr.Provider != "fal" || providerErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("ProviderError = %+v, want provider fal status 400", providerErr)
	}
	if !strings.Contains(providerErr.Detail, "invalid audio_url") {
		t.Errorf("ProviderError.Detail = %q, want response body detail", providerErr.Detail)
	}
}

func TestFalProvider_Infer_QwenTTS_ValidRequestAndResponse(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"audio":{"url":"https://storage.example.com/out.mp3","content_type":"audio/mpeg"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithAPIKey("test-key"), WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelQwenTTS,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.EmbeddingPart{URL: "https://storage.example.com/speaker.safetensors"},
				models.TextPart{Text: "Hello, how are you today?"},
			},
		}},
	}

	resp, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}

	// Validate outgoing request
	if transport.lastReq.URL.Path != "/fal-ai/qwen-3-tts/text-to-speech/1.7b" {
		t.Errorf("request URL path = %q, want /fal-ai/qwen-3-tts/text-to-speech/1.7b", transport.lastReq.URL.Path)
	}
	var body qwenTTSRequest
	if err := json.Unmarshal(transport.lastBody, &body); err != nil {
		t.Fatalf("request body JSON: %v", err)
	}
	if body.SpeakerEmbeddingURL != "https://storage.example.com/speaker.safetensors" {
		t.Errorf("request speaker_embedding_url = %q", body.SpeakerEmbeddingURL)
	}
	if body.Text != "Hello, how are you today?" {
		t.Errorf("request text = %q", body.Text)
	}

	// Validate response
	if len(resp.Message.ContentParts) != 2 {
		t.Fatalf("response ContentParts length = %d, want 2 (AudioPart + URL TextPart)", len(resp.Message.ContentParts))
	}
	ap, ok := resp.Message.ContentParts[0].(models.AudioPart)
	if !ok {
		t.Fatalf("response ContentParts[0] = %T, want models.AudioPart", resp.Message.ContentParts[0])
	}
	if ap.URL != "https://storage.example.com/out.mp3" {
		t.Errorf("AudioPart.URL = %q, want https://storage.example.com/out.mp3", ap.URL)
	}
	if ap.MediaType != "audio/mpeg" {
		t.Errorf("AudioPart.MediaType = %q, want audio/mpeg", ap.MediaType)
	}
	if resp.Message.TextContent() != "https://storage.example.com/out.mp3" {
		t.Errorf("Message.TextContent() = %q, want audio URL", resp.Message.TextContent())
	}
}

func TestFalProvider_Infer_QwenTTS_HTTPError(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 422,
		body:       `{"detail":"invalid speaker_embedding_url"}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelQwenTTS,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.EmbeddingPart{URL: "https://example.com/embed.safetensors"},
				models.TextPart{Text: "Say hello"},
			},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err == nil {
		t.Fatal("Infer() expected error on 422, got nil")
	}
	if !errors.Is(err, providers.ErrProviderRejected) {
		t.Fatalf("Infer() error = %v, want ErrProviderRejected", err)
	}
	if !errors.Is(err, providers.ErrInvalidRequest) {
		t.Fatalf("Infer() error = %v, want ErrInvalidRequest", err)
	}
}

// imageToVideoTestCase is one fal image-to-video model under test.
type imageToVideoTestCase struct {
	name      string
	model     string
	path      string
	prompt    string
	imageURL  string
	outputURL string
	mediaType string
	status    int
}

func imageToVideoTestCases() []imageToVideoTestCase {
	return []imageToVideoTestCase{
		{
			name: "GrokImagineVideo", model: ModelGrokImagineVideoImageToVideo,
			path: "/xai/grok-imagine-video/image-to-video", prompt: "Animate this photo",
			imageURL: "https://example.com/photo.png", outputURL: "https://storage.example.com/grok-out.mp4",
			mediaType: "image/png", status: http.StatusInternalServerError,
		},
		{
			name: "KlingVideoV3", model: ModelKlingVideoV3ImageToVideo,
			path: "/fal-ai/kling-video/v3/standard/image-to-video", prompt: "Animate this scene",
			imageURL: "https://example.com/scene.png", outputURL: "https://storage.example.com/kling-out.mp4",
			mediaType: "image/jpeg", status: http.StatusBadGateway,
		},
	}
}

func TestFalProvider_Infer_ImageToVideo_ValidRequestAndResponse(t *testing.T) {
	for _, tc := range imageToVideoTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			transport := &mockTransport{
				statusCode: http.StatusOK,
				body:       `{"video":{"url":"` + tc.outputURL + `","content_type":"video/mp4","file_name":"out.mp4"}}`,
			}
			p := New(WithAPIKey("test-key"), WithBaseURL("https://fal.run"), WithHTTPClient(&http.Client{Transport: transport}))
			resp, err := p.Infer(t.Context(), imageToVideoRequestFor(tc.model, models.TextPart{Text: tc.prompt}, models.ImagePart{URL: tc.imageURL}))
			if err != nil {
				t.Fatalf("Infer() unexpected error: %v", err)
			}
			assertFalRequest(t, transport, tc.path)
			var body imageToVideoRequest
			if err := json.Unmarshal(transport.lastBody, &body); err != nil {
				t.Fatalf("request body JSON: %v", err)
			}
			if body.Prompt != tc.prompt || body.ImageURL != tc.imageURL {
				t.Errorf("request body = %+v, want prompt %q image_url %q", body, tc.prompt, tc.imageURL)
			}
			assertFalVideoResponse(t, resp, tc.outputURL)
		})
	}
}

func TestFalProvider_Infer_ImageToVideo_InlineImageDataURI(t *testing.T) {
	for _, tc := range imageToVideoTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			transport := &mockTransport{statusCode: http.StatusOK, body: `{"video":{"url":"https://storage.example.com/out.mp4","content_type":"video/mp4"}}`}
			p := New(WithHTTPClient(&http.Client{Transport: transport}))
			image := models.ImagePart{Bytes: []byte("image-bytes"), MediaType: tc.mediaType}
			if _, err := p.Infer(t.Context(), imageToVideoRequestFor(tc.model, models.TextPart{Text: "Animate"}, image)); err != nil {
				t.Fatalf("Infer() unexpected error: %v", err)
			}
			var body imageToVideoRequest
			if err := json.Unmarshal(transport.lastBody, &body); err != nil {
				t.Fatalf("request body JSON: %v", err)
			}
			if !strings.HasPrefix(body.ImageURL, "data:"+tc.mediaType+";base64,") {
				t.Errorf("request image_url should be data URI, got %q", body.ImageURL)
			}
		})
	}
}

func TestFalProvider_Infer_ImageToVideo_RequiresImage(t *testing.T) {
	for _, tc := range imageToVideoTestCases() {
		t.Run(tc.name+"/structured text part", func(t *testing.T) {
			_, err := New().Infer(t.Context(), imageToVideoRequestFor(tc.model, models.TextPart{Text: "Animate this"}))
			if err == nil || !strings.Contains(err.Error(), "image_url is required") {
				t.Fatalf("Infer() error = %v, want image_url is required", err)
			}
		})
		t.Run(tc.name+"/plain text message", func(t *testing.T) {
			transport := &mockTransport{statusCode: http.StatusOK, body: `{"video":{"url":"https://storage.example.com/out.mp4","content_type":"video/mp4"}}`}
			p := New(WithHTTPClient(&http.Client{Transport: transport}))
			_, err := p.Infer(t.Context(), providers.InferenceRequest{
				Model:    tc.model,
				Messages: []models.Message{models.NewTextMessage(models.RoleUser, "animate something")},
			})
			if err == nil || !strings.Contains(err.Error(), "image_url is required") {
				t.Fatalf("Infer() error = %v, want image_url is required", err)
			}
		})
	}
}

func TestFalProvider_Infer_ImageToVideo_HTTPError(t *testing.T) {
	for _, tc := range imageToVideoTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			transport := &mockTransport{statusCode: tc.status, body: `{"detail":"upstream failure"}`}
			p := New(WithHTTPClient(&http.Client{Transport: transport}))
			_, err := p.Infer(t.Context(), imageToVideoRequestFor(tc.model, models.ImagePart{URL: tc.imageURL}))
			if err == nil {
				t.Fatalf("Infer() expected error on %d, got nil", tc.status)
			}
			if want := strconv.Itoa(tc.status); !strings.Contains(err.Error(), want) {
				t.Errorf("Infer() error = %v, want substring %s", err, want)
			}
		})
	}
}

// imageToVideoRequestFor builds a single user message request for model.
func imageToVideoRequestFor(model string, parts ...models.ContentPart) providers.InferenceRequest {
	return providers.InferenceRequest{
		Model:    model,
		Messages: []models.Message{{Role: models.RoleUser, ContentParts: parts}},
	}
}

// assertFalRequest checks the outgoing request path and API key header.
func assertFalRequest(t *testing.T, transport *mockTransport, wantPath string) {
	t.Helper()
	if transport.lastReq == nil {
		t.Fatal("no request was sent")
	}
	if transport.lastReq.URL.Path != wantPath {
		t.Errorf("request URL path = %q, want %s", transport.lastReq.URL.Path, wantPath)
	}
	if auth := transport.lastReq.Header.Get("Authorization"); auth != falTestAuthorization {
		t.Errorf("Authorization header = %q, want Key test-key", auth)
	}
}

// assertFalVideoResponse checks an assistant response carrying one video and
// its URL as text.
func assertFalVideoResponse(t *testing.T, resp providers.InferenceResponse, wantURL string) {
	t.Helper()
	if resp.Message.Role != models.RoleAssistant {
		t.Errorf("response Role = %q, want assistant", resp.Message.Role)
	}
	if len(resp.Message.ContentParts) != 2 {
		t.Fatalf("response ContentParts length = %d, want 2 (VideoPart + URL TextPart)", len(resp.Message.ContentParts))
	}
	vp, ok := resp.Message.ContentParts[0].(models.VideoPart)
	if !ok {
		t.Fatalf("response ContentParts[0] = %T, want models.VideoPart", resp.Message.ContentParts[0])
	}
	if vp.URL != wantURL || vp.MediaType != falTestVideoMediaType {
		t.Errorf("VideoPart = %+v, want URL %s media type %s", vp, wantURL, falTestVideoMediaType)
	}
	if resp.Message.TextContent() != wantURL {
		t.Errorf("Message.TextContent() = %q, want video URL", resp.Message.TextContent())
	}
}

// Fixture values: the test API key's Authorization header and the video media type.
const falTestAuthorization, falTestVideoMediaType = "Key test-key", "video/mp4"
