package fal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
	if req.Body != nil {
		m.lastBody, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(strings.NewReader(string(m.lastBody)))
	}
	return &http.Response{
		StatusCode: m.statusCode,
		Body:       io.NopCloser(strings.NewReader(m.body)),
		Header:     make(http.Header),
	}, nil
}

func TestFalProvider_Name(t *testing.T) {
	p := New()
	if got := p.Name(); got != "fal" {
		t.Errorf("Name() = %q, want %q", got, "fal")
	}
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
			name: "no user message",
			req: providers.InferenceRequest{
				Model:    ModelLTXAudioToVideo,
				Messages: []models.Message{models.NewTextMessage(models.RoleAssistant, "ok")},
			},
			wantErr: "no user message with audio or text found",
		},
		{
			name: "empty messages",
			req: providers.InferenceRequest{
				Model:    ModelLTXAudioToVideo,
				Messages: []models.Message{},
			},
			wantErr: "no user message with audio or text found",
		},
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
			if err == nil {
				t.Fatalf("Infer() expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Infer() error = %v, want substring %q", err, tt.wantErr)
			}
			if tt.wantClass != nil && !errors.Is(err, tt.wantClass) {
				t.Fatalf("Infer() error = %v, want class %v", err, tt.wantClass)
			}
			if tt.wantField != "" {
				var validationErr *providers.ValidationError
				if !errors.As(err, &validationErr) {
					t.Fatalf("Infer() error = %T, want ValidationError", err)
				}
				if validationErr.Provider != "fal" || validationErr.Feature != tt.wantField {
					t.Fatalf("ValidationError = %+v, want provider fal feature %q", validationErr, tt.wantField)
				}
			}
		})
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

	// Validate outgoing request
	if transport.lastReq == nil {
		t.Fatal("no request was sent")
	}
	if transport.lastReq.URL.Path != "/fal-ai/ltx-2-19b/audio-to-video" {
		t.Errorf("request URL path = %q, want /fal-ai/ltx-2-19b/audio-to-video", transport.lastReq.URL.Path)
	}
	if auth := transport.lastReq.Header.Get("Authorization"); auth != "Key test-key" {
		t.Errorf("Authorization header = %q, want Key test-key", auth)
	}
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

	// Validate response
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
	if vp.URL != "https://storage.example.com/out.mp4" {
		t.Errorf("VideoPart.URL = %q, want https://storage.example.com/out.mp4", vp.URL)
	}
	if vp.MediaType != "video/mp4" {
		t.Errorf("VideoPart.MediaType = %q, want video/mp4", vp.MediaType)
	}
	if resp.Message.TextContent() != "https://storage.example.com/out.mp4" {
		t.Errorf("Message.TextContent() = %q, want video URL", resp.Message.TextContent())
	}
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
	if providerErr.Provider != "fal" || providerErr.StatusCode != 400 {
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

func TestFalProvider_Infer_QwenTTS_MissingEmbedding(t *testing.T) {
	ctx := context.Background()
	p := New()

	req := providers.InferenceRequest{
		Model: ModelQwenTTS,
		Messages: []models.Message{{
			Role:         models.RoleUser,
			ContentParts: []models.ContentPart{models.TextPart{Text: "Say hello"}},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err == nil {
		t.Fatal("Infer() expected error for missing embedding, got nil")
	}
}

func TestFalProvider_Infer_GrokImagineVideo_ValidRequestAndResponse(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"video":{"url":"https://storage.example.com/grok-out.mp4","content_type":"video/mp4","file_name":"grok-out.mp4"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithAPIKey("test-key"), WithBaseURL("https://fal.run"), WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelGrokImagineVideoImageToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.TextPart{Text: "Animate this photo"},
				models.ImagePart{URL: "https://example.com/photo.png"},
			},
		}},
	}

	resp, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}

	// Validate outgoing request
	if transport.lastReq == nil {
		t.Fatal("no request was sent")
	}
	if transport.lastReq.URL.Path != "/xai/grok-imagine-video/image-to-video" {
		t.Errorf("request URL path = %q, want /xai/grok-imagine-video/image-to-video", transport.lastReq.URL.Path)
	}
	if auth := transport.lastReq.Header.Get("Authorization"); auth != "Key test-key" {
		t.Errorf("Authorization header = %q, want Key test-key", auth)
	}
	var body grokImagineVideoRequest
	if err := json.Unmarshal(transport.lastBody, &body); err != nil {
		t.Fatalf("request body JSON: %v", err)
	}
	if body.Prompt != "Animate this photo" {
		t.Errorf("request prompt = %q, want %q", body.Prompt, "Animate this photo")
	}
	if body.ImageURL != "https://example.com/photo.png" {
		t.Errorf("request image_url = %q, want https://example.com/photo.png", body.ImageURL)
	}

	// Validate response
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
	if vp.URL != "https://storage.example.com/grok-out.mp4" {
		t.Errorf("VideoPart.URL = %q, want https://storage.example.com/grok-out.mp4", vp.URL)
	}
	if vp.MediaType != "video/mp4" {
		t.Errorf("VideoPart.MediaType = %q, want video/mp4", vp.MediaType)
	}
	if resp.Message.TextContent() != "https://storage.example.com/grok-out.mp4" {
		t.Errorf("Message.TextContent() = %q, want video URL", resp.Message.TextContent())
	}
}

func TestFalProvider_Infer_GrokImagineVideo_InlineImageDataURI(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"video":{"url":"https://storage.example.com/out.mp4","content_type":"video/mp4"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelGrokImagineVideoImageToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.TextPart{Text: "Animate"},
				models.ImagePart{Bytes: []byte("png-bytes"), MediaType: "image/png"},
			},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}
	var body grokImagineVideoRequest
	if err := json.Unmarshal(transport.lastBody, &body); err != nil {
		t.Fatalf("request body JSON: %v", err)
	}
	if !strings.HasPrefix(body.ImageURL, "data:image/png;base64,") {
		t.Errorf("request image_url should be data URI, got %q", body.ImageURL)
	}
}

func TestFalProvider_Infer_GrokImagineVideo_MissingImage(t *testing.T) {
	ctx := context.Background()
	p := New()

	req := providers.InferenceRequest{
		Model: ModelGrokImagineVideoImageToVideo,
		Messages: []models.Message{{
			Role:         models.RoleUser,
			ContentParts: []models.ContentPart{models.TextPart{Text: "Animate this"}},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err == nil {
		t.Fatal("Infer() expected error for missing image, got nil")
	}
	if !strings.Contains(err.Error(), "image_url is required") {
		t.Errorf("Infer() error = %v, want substring image_url is required", err)
	}
}

func TestFalProvider_Infer_GrokImagineVideo_HTTPError(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 500,
		body:       `{"detail":"internal error"}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelGrokImagineVideoImageToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.ImagePart{URL: "https://example.com/photo.png"},
			},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err == nil {
		t.Fatal("Infer() expected error on 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("Infer() error = %v, want substring 500", err)
	}
}

func TestFalProvider_Infer_GrokImagineVideo_PromptOnlyNoImage(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"video":{"url":"https://storage.example.com/out.mp4","content_type":"video/mp4"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	// Prompt without image — should extract text but fail on missing image_url
	req := providers.InferenceRequest{
		Model: ModelGrokImagineVideoImageToVideo,
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "animate something"),
		},
	}

	_, err := p.Infer(ctx, req)
	if err == nil {
		t.Fatal("Infer() expected error for text-only (no image), got nil")
	}
	if !strings.Contains(err.Error(), "image_url is required") {
		t.Errorf("Infer() error = %v, want image_url is required", err)
	}
}

func TestFalProvider_Infer_KlingVideoV3_ValidRequestAndResponse(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"video":{"url":"https://storage.example.com/kling-out.mp4","content_type":"video/mp4","file_name":"kling-out.mp4"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithAPIKey("test-key"), WithBaseURL("https://fal.run"), WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelKlingVideoV3ImageToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.TextPart{Text: "Animate this scene"},
				models.ImagePart{URL: "https://example.com/scene.png"},
			},
		}},
	}

	resp, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}

	// Validate outgoing request
	if transport.lastReq == nil {
		t.Fatal("no request was sent")
	}
	if transport.lastReq.URL.Path != "/fal-ai/kling-video/v3/standard/image-to-video" {
		t.Errorf("request URL path = %q, want /fal-ai/kling-video/v3/standard/image-to-video", transport.lastReq.URL.Path)
	}
	if auth := transport.lastReq.Header.Get("Authorization"); auth != "Key test-key" {
		t.Errorf("Authorization header = %q, want Key test-key", auth)
	}
	var body klingVideoV3Request
	if err := json.Unmarshal(transport.lastBody, &body); err != nil {
		t.Fatalf("request body JSON: %v", err)
	}
	if body.Prompt != "Animate this scene" {
		t.Errorf("request prompt = %q, want %q", body.Prompt, "Animate this scene")
	}
	if body.ImageURL != "https://example.com/scene.png" {
		t.Errorf("request image_url = %q, want https://example.com/scene.png", body.ImageURL)
	}

	// Validate response
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
	if vp.URL != "https://storage.example.com/kling-out.mp4" {
		t.Errorf("VideoPart.URL = %q, want https://storage.example.com/kling-out.mp4", vp.URL)
	}
	if vp.MediaType != "video/mp4" {
		t.Errorf("VideoPart.MediaType = %q, want video/mp4", vp.MediaType)
	}
	if resp.Message.TextContent() != "https://storage.example.com/kling-out.mp4" {
		t.Errorf("Message.TextContent() = %q, want video URL", resp.Message.TextContent())
	}
}

func TestFalProvider_Infer_KlingVideoV3_InlineImageDataURI(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"video":{"url":"https://storage.example.com/out.mp4","content_type":"video/mp4"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelKlingVideoV3ImageToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.TextPart{Text: "Animate"},
				models.ImagePart{Bytes: []byte("jpeg-bytes"), MediaType: "image/jpeg"},
			},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}
	var body klingVideoV3Request
	if err := json.Unmarshal(transport.lastBody, &body); err != nil {
		t.Fatalf("request body JSON: %v", err)
	}
	if !strings.HasPrefix(body.ImageURL, "data:image/jpeg;base64,") {
		t.Errorf("request image_url should be data URI, got %q", body.ImageURL)
	}
}

func TestFalProvider_Infer_KlingVideoV3_MissingImage(t *testing.T) {
	ctx := context.Background()
	p := New()

	req := providers.InferenceRequest{
		Model: ModelKlingVideoV3ImageToVideo,
		Messages: []models.Message{{
			Role:         models.RoleUser,
			ContentParts: []models.ContentPart{models.TextPart{Text: "Animate this"}},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err == nil {
		t.Fatal("Infer() expected error for missing image, got nil")
	}
	if !strings.Contains(err.Error(), "image_url is required") {
		t.Errorf("Infer() error = %v, want substring image_url is required", err)
	}
}

func TestFalProvider_Infer_KlingVideoV3_HTTPError(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 502,
		body:       `{"detail":"bad gateway"}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelKlingVideoV3ImageToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.ImagePart{URL: "https://example.com/photo.png"},
			},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err == nil {
		t.Fatal("Infer() expected error on 502, got nil")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("Infer() error = %v, want substring 502", err)
	}
}

func TestFalProvider_Infer_KlingVideoV3_PromptOnlyNoImage(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"video":{"url":"https://storage.example.com/out.mp4","content_type":"video/mp4"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelKlingVideoV3ImageToVideo,
		Messages: []models.Message{
			models.NewTextMessage(models.RoleUser, "animate something"),
		},
	}

	_, err := p.Infer(ctx, req)
	if err == nil {
		t.Fatal("Infer() expected error for text-only (no image), got nil")
	}
	if !strings.Contains(err.Error(), "image_url is required") {
		t.Errorf("Infer() error = %v, want image_url is required", err)
	}
}
