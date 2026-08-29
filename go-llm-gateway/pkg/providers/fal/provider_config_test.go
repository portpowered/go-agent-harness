package fal

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestFalProvider_ConfigPassthrough_GrokImagineVideo(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"video":{"url":"https://storage.example.com/out.mp4","content_type":"video/mp4"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithAPIKey("test-key"), WithHTTPClient(client))

	req := providers.InferenceRequest{
		Model: ModelGrokImagineVideoImageToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.TextPart{Text: "animate this"},
				models.ImagePart{URL: "https://example.com/photo.jpg"},
			},
		}},
		Config: json.RawMessage(`{"aspect_ratio":"16:9","duration":5,"cfg_scale":0.7}`),
	}

	_, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}

	// Verify config params appear in the outgoing HTTP request body
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(transport.lastBody, &sent); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	// Original fields must be present
	if string(sent["image_url"]) != `"https://example.com/photo.jpg"` {
		t.Errorf("image_url = %s, want %q", sent["image_url"], "https://example.com/photo.jpg")
	}
	if string(sent["prompt"]) != `"animate this"` {
		t.Errorf("prompt = %s, want %q", sent["prompt"], "animate this")
	}
	// Config fields must be merged in
	if string(sent["aspect_ratio"]) != `"16:9"` {
		t.Errorf("aspect_ratio = %s, want %q", sent["aspect_ratio"], "16:9")
	}
	if string(sent["duration"]) != `5` {
		t.Errorf("duration = %s, want 5", sent["duration"])
	}
	if string(sent["cfg_scale"]) != `0.7` {
		t.Errorf("cfg_scale = %s, want 0.7", sent["cfg_scale"])
	}
}

func TestFalProvider_ConfigPassthrough_KlingVideoV3(t *testing.T) {
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
				models.ImagePart{URL: "https://example.com/img.png"},
			},
		}},
		Config: json.RawMessage(`{"duration":10,"negative_prompt":"blurry"}`),
	}

	_, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}

	var sent map[string]json.RawMessage
	if err := json.Unmarshal(transport.lastBody, &sent); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if string(sent["image_url"]) != `"https://example.com/img.png"` {
		t.Errorf("image_url = %s, want %q", sent["image_url"], "https://example.com/img.png")
	}
	if string(sent["duration"]) != `10` {
		t.Errorf("duration = %s, want 10", sent["duration"])
	}
	if string(sent["negative_prompt"]) != `"blurry"` {
		t.Errorf("negative_prompt = %s, want %q", sent["negative_prompt"], "blurry")
	}
}

func TestFalProvider_ConfigPassthrough_NilConfig(t *testing.T) {
	ctx := context.Background()
	transport := &mockTransport{
		statusCode: 200,
		body:       `{"video":{"url":"https://storage.example.com/out.mp4","content_type":"video/mp4"}}`,
	}
	client := &http.Client{Transport: transport}
	p := New(WithHTTPClient(client))

	// No Config set — should work normally (backward compatible)
	req := providers.InferenceRequest{
		Model: ModelGrokImagineVideoImageToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.ImagePart{URL: "https://example.com/photo.jpg"},
			},
		}},
	}

	_, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}

	// Body should contain only the standard fields
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(transport.lastBody, &sent); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if _, ok := sent["image_url"]; !ok {
		t.Error("expected image_url in request body")
	}
	// Should have only 1 field (image_url; prompt is omitempty and empty here)
	if len(sent) != 1 {
		t.Errorf("expected 1 field in body, got %d: %v", len(sent), sent)
	}
}

func TestFalProvider_ConfigPassthrough_LTXAudioToVideo(t *testing.T) {
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
				models.TextPart{Text: "A person talks"},
				models.AudioPart{URL: "https://example.com/audio.mp3"},
			},
		}},
		Config: json.RawMessage(`{"num_frames":120}`),
	}

	_, err := p.Infer(ctx, req)
	if err != nil {
		t.Fatalf("Infer() unexpected error: %v", err)
	}

	var sent map[string]json.RawMessage
	if err := json.Unmarshal(transport.lastBody, &sent); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if string(sent["audio_url"]) != `"https://example.com/audio.mp3"` {
		t.Errorf("audio_url = %s, want %q", sent["audio_url"], "https://example.com/audio.mp3")
	}
	if string(sent["num_frames"]) != `120` {
		t.Errorf("num_frames = %s, want 120", sent["num_frames"])
	}
}
