package fal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const (
	defaultBaseURL = "https://fal.run"

	// Model IDs for fal.ai endpoints.
	ModelLTXAudioToVideo              = "fal-ai/ltx-2-19b/audio-to-video"
	ModelQwenCloneVoice               = "fal-ai/qwen-3-tts/clone-voice/1.7b"
	ModelQwenTTS                      = "fal-ai/qwen-3-tts/text-to-speech/1.7b"
	ModelGrokImagineVideoImageToVideo = "xai/grok-imagine-video/image-to-video"
	ModelKlingVideoV3ImageToVideo     = "fal-ai/kling-video/v3/standard/image-to-video"
)

// FalProvider implements the Provider interface for fal.ai model endpoints
// (LTX audio-to-video, Qwen 3 TTS clone-voice, Qwen 3 TTS text-to-speech). It uses FAL_KEY for auth.
type FalProvider struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// New creates a new fal.ai provider.
func New(opts ...Option) *FalProvider {
	p := &FalProvider{
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *FalProvider) Name() string {
	return "fal"
}

func (p *FalProvider) Capabilities() providers.ProviderCapabilities {
	sessionUnsupported := "fal.ai endpoints in this wrapper are synchronous stateless endpoints only"
	sessionCap := capabilities.Unsupported(sessionUnsupported)
	return capabilities.ProviderCapabilities{
		Provider: p.Name(),
		Stateless: capabilities.StatelessCapabilities{
			Tools:                  capabilities.Unsupported("fal.ai model endpoints in this wrapper do not accept gateway tool definitions"),
			Streaming:              capabilities.Unsupported("fal.ai endpoints in this wrapper are sync-only"),
			ImageInput:             capabilities.Supported("image input is mapped for image-to-video models"),
			AudioInput:             capabilities.Supported("audio input is mapped for audio and voice models"),
			AudioOutput:            capabilities.Supported("audio output is normalized for speech models"),
			VideoOutput:            capabilities.Supported("video output is normalized for video-generation models"),
			Reasoning:              capabilities.Unsupported("fal.ai model endpoints in this wrapper do not accept reasoning options"),
			PromptCaching:          capabilities.Unsupported("fal.ai model endpoints in this wrapper do not accept prompt cache-control options"),
			ProviderSpecificConfig: capabilities.Supported("InferenceRequest Config is merged into fal.ai request payloads"),
		},
		Session: capabilities.SessionCapabilities{
			Sessions:               sessionCap,
			Tools:                  sessionCap,
			AudioInput:             sessionCap,
			AudioOutput:            sessionCap,
			ProviderSpecificConfig: sessionCap,
		},
	}
}

func (p *FalProvider) Infer(ctx context.Context, req providers.InferenceRequest) (providers.InferenceResponse, error) {
	model := req.Model
	if model == "" {
		return providers.InferenceResponse{}, providers.NewInvalidRequestError("fal", "model", fmt.Sprintf("fal provider requires Model to be set (e.g. %q, %q, or %q)", ModelLTXAudioToVideo, ModelQwenCloneVoice, ModelQwenTTS))
	}

	config := req.Config

	switch model {
	case ModelQwenTTS:
		embeddingURL, text, err := extractEmbeddingAndTextFromMessages(req.Messages)
		if err != nil {
			return providers.InferenceResponse{}, err
		}
		return p.inferQwenTTS(ctx, qwenTTSRequest{SpeakerEmbeddingURL: embeddingURL, Text: text}, config)
	case ModelGrokImagineVideoImageToVideo:
		imageURL, text, err := extractImageAndTextFromMessages(req.Messages)
		if err != nil {
			return providers.InferenceResponse{}, err
		}
		return p.inferGrokImagineVideo(ctx, imageURL, text, config)
	case ModelKlingVideoV3ImageToVideo:
		imageURL, text, err := extractImageAndTextFromMessages(req.Messages)
		if err != nil {
			return providers.InferenceResponse{}, err
		}
		return p.inferKlingVideoV3(ctx, imageURL, text, config)
	default:
		audioURL, text, err := extractAudioAndTextFromMessages(req.Messages)
		if err != nil {
			return providers.InferenceResponse{}, err
		}
		switch model {
		case ModelLTXAudioToVideo:
			return p.inferLTXAudioToVideo(ctx, audioURL, text, config)
		case ModelQwenCloneVoice:
			return p.inferQwenCloneVoice(ctx, audioURL, text, config)
		default:
			supported := []string{ModelLTXAudioToVideo, ModelQwenCloneVoice, ModelQwenTTS, ModelGrokImagineVideoImageToVideo, ModelKlingVideoV3ImageToVideo}
			return providers.InferenceResponse{}, providers.NewUnsupportedRequestError("fal", "model", model, supported, fmt.Sprintf("fal provider: unsupported model %q (supported: %q, %q, %q, %q, %q)", model, ModelLTXAudioToVideo, ModelQwenCloneVoice, ModelQwenTTS, ModelGrokImagineVideoImageToVideo, ModelKlingVideoV3ImageToVideo))
		}
	}
}

func (p *FalProvider) InferStream(ctx context.Context, req providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	capability := p.Capabilities().Stateless.Streaming
	return nil, &providers.UnsupportedFeatureError{
		Provider:      p.Name(),
		Feature:       capabilities.FeatureStreaming,
		RequestedMode: capabilities.RequestedModeStatelessStream,
		Capability:    capability,
	}
}

// extractAudioAndTextFromMessages takes the last user message and returns an audio URL
// (or data URI from inline bytes) and concatenated text from text parts.
func extractAudioAndTextFromMessages(msgs []models.Message) (audioURL, text string, err error) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != models.RoleUser {
			continue
		}
		var parts []models.ContentPart
		if len(msgs[i].ContentParts) > 0 {
			parts = msgs[i].ContentParts
		} else if msgs[i].TextContent() != "" {
			parts = []models.ContentPart{models.TextPart{Text: msgs[i].TextContent()}}
		}
		for _, part := range parts {
			switch v := part.(type) {
			case models.TextPart:
				if text != "" {
					text += " "
				}
				text += v.Text
			case models.AudioPart:
				if audioURL != "" {
					continue
				}
				audioURL = v.URL
				if len(v.Bytes) > 0 {
					mediaType := v.MediaType
					if mediaType == "" {
						mediaType = "audio/mpeg"
					}
					audioURL = "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(v.Bytes)
				}
			}
		}
		if audioURL != "" || text != "" {
			return audioURL, text, nil
		}
	}
	return "", "", fmt.Errorf("fal provider: no user message with audio or text found")
}

// extractEmbeddingAndTextFromMessages scans messages from the end, finds the last user message,
// extracts an EmbeddingPart URL (converting inline bytes to a data URI) and concatenated text parts.
func extractEmbeddingAndTextFromMessages(msgs []models.Message) (embeddingURL, text string, err error) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != models.RoleUser {
			continue
		}
		var parts []models.ContentPart
		if len(msgs[i].ContentParts) > 0 {
			parts = msgs[i].ContentParts
		} else if msgs[i].TextContent() != "" {
			parts = []models.ContentPart{models.TextPart{Text: msgs[i].TextContent()}}
		}
		for _, part := range parts {
			switch v := part.(type) {
			case models.TextPart:
				if text != "" {
					text += " "
				}
				text += v.Text
			case models.EmbeddingPart:
				if embeddingURL != "" {
					continue
				}
				embeddingURL = v.URL
				if len(v.Bytes) > 0 {
					mediaType := v.MediaType
					if mediaType == "" {
						mediaType = "application/octet-stream"
					}
					embeddingURL = "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(v.Bytes)
				}
			}
		}
		if embeddingURL != "" || text != "" {
			return embeddingURL, text, nil
		}
	}
	return "", "", fmt.Errorf("fal provider: no user message with embedding or text found")
}

// extractImageAndTextFromMessages takes the last user message and returns an image URL
// (or data URI from inline bytes) and concatenated text from text parts.
func extractImageAndTextFromMessages(msgs []models.Message) (imageURL, text string, err error) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != models.RoleUser {
			continue
		}
		var parts []models.ContentPart
		if len(msgs[i].ContentParts) > 0 {
			parts = msgs[i].ContentParts
		} else if msgs[i].TextContent() != "" {
			parts = []models.ContentPart{models.TextPart{Text: msgs[i].TextContent()}}
		}
		for _, part := range parts {
			switch v := part.(type) {
			case models.TextPart:
				if text != "" {
					text += " "
				}
				text += v.Text
			case models.ImagePart:
				if imageURL != "" {
					continue
				}
				imageURL = v.URL
				if len(v.Bytes) > 0 {
					mediaType := v.MediaType
					if mediaType == "" {
						mediaType = "image/png"
					}
					imageURL = "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(v.Bytes)
				}
			}
		}
		if imageURL != "" || text != "" {
			return imageURL, text, nil
		}
	}
	return "", "", fmt.Errorf("fal provider: no user message with image or text found")
}

// --- LTX audio-to-video ---

type ltxAudioToVideoRequest struct {
	Prompt   string `json:"prompt"`
	AudioURL string `json:"audio_url"`
}

type ltxVideoFile struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	FileName    string `json:"file_name"`
}

type ltxAudioToVideoResponse struct {
	Video ltxVideoFile `json:"video"`
}

func (p *FalProvider) inferLTXAudioToVideo(ctx context.Context, audioURL, prompt string, config json.RawMessage) (providers.InferenceResponse, error) {
	if audioURL == "" {
		return providers.InferenceResponse{}, providers.NewInvalidRequestError("fal", "audio_url", "fal LTX audio-to-video: audio_url is required")
	}
	if prompt == "" {
		prompt = "A person speaks to the camera"
	}
	body := ltxAudioToVideoRequest{Prompt: prompt, AudioURL: audioURL}
	var resp ltxAudioToVideoResponse
	if err := p.doJSON(ctx, ModelLTXAudioToVideo, body, &resp, config); err != nil {
		return providers.InferenceResponse{}, err
	}
	parts := []models.ContentPart{
		models.VideoPart{
			URL:       resp.Video.URL,
			MediaType: resp.Video.ContentType,
		},
	}
	if resp.Video.URL != "" {
		parts = append(parts, models.TextPart{Text: resp.Video.URL})
	}
	msg := models.Message{
		Role:         models.RoleAssistant,
		ContentParts: parts,
	}
	return providers.InferenceResponse{Message: msg, Usage: models.TokenUsage{}}, nil
}

// --- Grok Imagine Video image-to-video ---

type grokImagineVideoRequest struct {
	ImageURL string `json:"image_url"`
	Prompt   string `json:"prompt,omitempty"`
}

type grokImagineVideoFile struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	FileName    string `json:"file_name"`
}

type grokImagineVideoResponse struct {
	Video grokImagineVideoFile `json:"video"`
}

func (p *FalProvider) inferGrokImagineVideo(ctx context.Context, imageURL, prompt string, config json.RawMessage) (providers.InferenceResponse, error) {
	if imageURL == "" {
		return providers.InferenceResponse{}, providers.NewInvalidRequestError("fal", "image_url", "fal Grok Imagine Video: image_url is required")
	}
	body := grokImagineVideoRequest{ImageURL: imageURL, Prompt: prompt}
	var resp grokImagineVideoResponse
	if err := p.doJSON(ctx, ModelGrokImagineVideoImageToVideo, body, &resp, config); err != nil {
		return providers.InferenceResponse{}, err
	}
	parts := []models.ContentPart{
		models.VideoPart{
			URL:       resp.Video.URL,
			MediaType: resp.Video.ContentType,
		},
	}
	if resp.Video.URL != "" {
		parts = append(parts, models.TextPart{Text: resp.Video.URL})
	}
	msg := models.Message{
		Role:         models.RoleAssistant,
		ContentParts: parts,
	}
	return providers.InferenceResponse{Message: msg, Usage: models.TokenUsage{}}, nil
}

// --- Kling Video v3 image-to-video ---

type klingVideoV3Request struct {
	ImageURL string `json:"image_url"`
	Prompt   string `json:"prompt,omitempty"`
}

type klingVideoV3File struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	FileName    string `json:"file_name"`
}

type klingVideoV3Response struct {
	Video klingVideoV3File `json:"video"`
}

func (p *FalProvider) inferKlingVideoV3(ctx context.Context, imageURL, prompt string, config json.RawMessage) (providers.InferenceResponse, error) {
	if imageURL == "" {
		return providers.InferenceResponse{}, providers.NewInvalidRequestError("fal", "image_url", "fal Kling Video v3: image_url is required")
	}
	body := klingVideoV3Request{ImageURL: imageURL, Prompt: prompt}
	var resp klingVideoV3Response
	if err := p.doJSON(ctx, ModelKlingVideoV3ImageToVideo, body, &resp, config); err != nil {
		return providers.InferenceResponse{}, err
	}
	parts := []models.ContentPart{
		models.VideoPart{
			URL:       resp.Video.URL,
			MediaType: resp.Video.ContentType,
		},
	}
	if resp.Video.URL != "" {
		parts = append(parts, models.TextPart{Text: resp.Video.URL})
	}
	msg := models.Message{
		Role:         models.RoleAssistant,
		ContentParts: parts,
	}
	return providers.InferenceResponse{Message: msg, Usage: models.TokenUsage{}}, nil
}

// --- Qwen 3 TTS clone-voice ---

type qwenCloneVoiceRequest struct {
	AudioURL      string `json:"audio_url"`
	ReferenceText string `json:"reference_text,omitempty"`
}

type qwenFile struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	FileName    string `json:"file_name"`
}

type qwenCloneVoiceResponse struct {
	SpeakerEmbedding qwenFile `json:"speaker_embedding"`
}

func (p *FalProvider) inferQwenCloneVoice(ctx context.Context, audioURL, referenceText string, config json.RawMessage) (providers.InferenceResponse, error) {
	if audioURL == "" {
		return providers.InferenceResponse{}, providers.NewInvalidRequestError("fal", "audio_url", "fal Qwen clone-voice: audio_url is required")
	}
	body := qwenCloneVoiceRequest{AudioURL: audioURL, ReferenceText: referenceText}
	var resp qwenCloneVoiceResponse
	if err := p.doJSON(ctx, ModelQwenCloneVoice, body, &resp, config); err != nil {
		return providers.InferenceResponse{}, err
	}
	parts := []models.ContentPart{
		models.EmbeddingPart{
			URL:       resp.SpeakerEmbedding.URL,
			MediaType: resp.SpeakerEmbedding.ContentType,
		},
	}
	if resp.SpeakerEmbedding.URL != "" {
		parts = append(parts, models.TextPart{Text: resp.SpeakerEmbedding.URL})
	}
	msg := models.Message{
		Role:         models.RoleAssistant,
		ContentParts: parts,
	}
	return providers.InferenceResponse{Message: msg, Usage: models.TokenUsage{}}, nil
}

// --- Qwen 3 TTS text-to-speech ---

// https://fal.ai/models/fal-ai/qwen-3-tts/text-to-speech/1.7b/api
type qwenTTSRequest struct {
	SpeakerEmbeddingURL string `json:"speaker_voice_embedding_file_url,omitempty"`
	Voice               string `json:"voice,omitempty"` // e.g. " Vivian, Serena, Uncle_Fu, Dylan, Eric, Ryan, Aiden, Ono_Anna, Sohee (for embedding-based)
	Text                string `json:"text"`
}

type qwenTTSVoice string

const (
	QwenVoiceVivian  qwenTTSVoice = "Vivian"
	QwenVoiceSerena  qwenTTSVoice = "Serena"
	QwenVoiceUncleFu qwenTTSVoice = "Uncle_Fu"
	QwenVoiceDylan   qwenTTSVoice = "Dylan"
	QwenVoiceEric    qwenTTSVoice = "Eric"
	QwenVoiceRyan    qwenTTSVoice = "Ryan"
	QwenVoiceAiden   qwenTTSVoice = "Aiden"
	QwenVoiceOnoAnna qwenTTSVoice = "Ono_Anna"
	QwenVoiceSohee   qwenTTSVoice = "Sohee"
)

type qwenTTSAudioFile struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

type qwenTTSResponse struct {
	Audio qwenTTSAudioFile `json:"audio"`
}

func (p *FalProvider) inferQwenTTS(ctx context.Context, request qwenTTSRequest, config json.RawMessage) (providers.InferenceResponse, error) {
	body := request
	if request.SpeakerEmbeddingURL == "" && request.Voice == "" {
		body.Voice = string(QwenVoiceVivian)
	}

	var resp qwenTTSResponse
	if err := p.doJSON(ctx, ModelQwenTTS, body, &resp, config); err != nil {
		return providers.InferenceResponse{}, err
	}
	parts := []models.ContentPart{
		models.AudioPart{
			URL:       resp.Audio.URL,
			MediaType: resp.Audio.ContentType,
		},
	}
	if resp.Audio.URL != "" {
		parts = append(parts, models.TextPart{Text: resp.Audio.URL})
	}
	msg := models.Message{
		Role:         models.RoleAssistant,
		ContentParts: parts,
	}
	return providers.InferenceResponse{Message: msg, Usage: models.TokenUsage{}}, nil
}

func (p *FalProvider) doJSON(ctx context.Context, modelID string, body any, result any, config ...json.RawMessage) error {
	url := p.baseURL + "/" + modelID
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("fal: encode request: %w", err)
	}
	// Merge model-specific config parameters into the request body.
	if len(config) > 0 && len(config[0]) > 0 {
		var base map[string]json.RawMessage
		if err := json.Unmarshal(payload, &base); err != nil {
			return fmt.Errorf("fal: merge config: unmarshal base: %w", err)
		}
		var extra map[string]json.RawMessage
		if err := json.Unmarshal(config[0], &extra); err != nil {
			return fmt.Errorf("fal: merge config: unmarshal config: %w", err)
		}
		maps.Copy(base, extra)
		payload, err = json.Marshal(base)
		if err != nil {
			return fmt.Errorf("fal: merge config: re-marshal: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("fal: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Key "+p.apiKey)
	}
	client := p.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fal: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return providers.NewProviderHTTPError("fal", resp.StatusCode, fmt.Sprintf("%s %s: %d %s", req.Method, url, resp.StatusCode, string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("fal: decode response: %w", err)
	}
	return nil
}
