package config

import "testing"

func TestModelInfo_SupportsOutputModality(t *testing.T) {
	textOnly := ModelInfo{
		Name:             "gpt-4o",
		OutputModalities: []string{"text"},
	}
	imageCapable := ModelInfo{
		Name:             "google/gemini-2.0-flash",
		OutputModalities: []string{"text", "image"},
	}
	multiModal := ModelInfo{
		Name:             "future-model",
		OutputModalities: []string{"text", "image", "audio", "video", "embedding"},
	}

	tests := []struct {
		name     string
		model    ModelInfo
		modality string
		want     bool
	}{
		{"text-only model supports text", textOnly, "text", true},
		{"text-only model rejects image", textOnly, "image", false},
		{"text-only model rejects audio", textOnly, "audio", false},
		{"text-only model rejects video", textOnly, "video", false},
		{"text-only model rejects embedding", textOnly, "embedding", false},
		{"image-capable model supports text", imageCapable, "text", true},
		{"image-capable model supports image", imageCapable, "image", true},
		{"image-capable model rejects audio", imageCapable, "audio", false},
		{"multimodal model supports image", multiModal, "image", true},
		{"multimodal model supports audio", multiModal, "audio", true},
		{"multimodal model supports video", multiModal, "video", true},
		{"multimodal model supports embedding", multiModal, "embedding", true},
		{"empty modality is not supported", textOnly, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.model.SupportsOutputModality(tt.modality)
			if got != tt.want {
				t.Errorf("SupportsOutputModality(%q) = %v, want %v", tt.modality, got, tt.want)
			}
		})
	}
}

func TestModelInfo_SupportsInputMimeType(t *testing.T) {
	withTypes := ModelInfo{
		Name:                    "test-model",
		SupportedInputMimeTypes: []string{"image/png", "image/jpeg"},
	}
	withoutTypes := ModelInfo{
		Name: "open-model",
	}
	emptyTypes := ModelInfo{
		Name:                    "empty-model",
		SupportedInputMimeTypes: []string{},
	}

	tests := []struct {
		name     string
		model    ModelInfo
		mimeType string
		want     bool
	}{
		{"supported type passes", withTypes, "image/png", true},
		{"supported type passes (jpeg)", withTypes, "image/jpeg", true},
		{"unsupported type fails", withTypes, "image/webp", false},
		{"unsupported type fails (gif)", withTypes, "image/gif", false},
		{"nil list accepts all", withoutTypes, "image/webp", true},
		{"nil list accepts unknown", withoutTypes, "application/octet-stream", true},
		{"empty list accepts all", emptyTypes, "image/webp", true},
		{"empty string mime on restricted model", withTypes, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.model.SupportsInputMimeType(tt.mimeType)
			if got != tt.want {
				t.Errorf("SupportsInputMimeType(%q) = %v, want %v", tt.mimeType, got, tt.want)
			}
		})
	}
}

func TestDefaultModelsConfig_AllMultimodalModelsHaveMimeTypes(t *testing.T) {
	cfg := defaultModelsConfig()
	for _, m := range cfg.Models {
		hasNonTextInput := false
		for _, mod := range m.InputModalities {
			if mod != "text" && mod != "embedding" {
				hasNonTextInput = true
				break
			}
		}
		if hasNonTextInput && len(m.SupportedInputMimeTypes) == 0 {
			t.Errorf("model %q accepts non-text input modalities %v but has no SupportedInputMimeTypes", m.Name, m.InputModalities)
		}
	}
}

func TestModelsConfig_Lookup(t *testing.T) {
	cfg := &ModelsConfig{
		Models: []ModelInfo{
			{Name: "gpt-4o", Aliases: []string{"gpt-4o-2024-11-20"}, OutputModalities: []string{"text"}},
			{Name: "google/gemini-2.0-flash", Aliases: []string{"google/gemini-2.0-flash-001"}, OutputModalities: []string{"text", "image"}},
		},
	}

	t.Run("lookup by canonical name", func(t *testing.T) {
		m := cfg.Lookup("gpt-4o")
		if m == nil || m.Name != "gpt-4o" {
			t.Errorf("expected gpt-4o, got %v", m)
		}
	})

	t.Run("lookup by alias", func(t *testing.T) {
		m := cfg.Lookup("gpt-4o-2024-11-20")
		if m == nil || m.Name != "gpt-4o" {
			t.Errorf("expected gpt-4o via alias, got %v", m)
		}
	})

	t.Run("unknown model returns nil", func(t *testing.T) {
		m := cfg.Lookup("nonexistent-model")
		if m != nil {
			t.Errorf("expected nil, got %v", m)
		}
	})
}

func TestModelsConfig_ValidationScenarios(t *testing.T) {
	cfg := &ModelsConfig{
		Models: []ModelInfo{
			{Name: "text-only-model", OutputModalities: []string{"text"}},
			{Name: "image-model", OutputModalities: []string{"text", "image"}},
			{Name: "audio-model", OutputModalities: []string{"text", "audio"}},
			{Name: "full-model", OutputModalities: []string{"text", "image", "audio", "video", "embedding"}},
		},
	}

	tests := []struct {
		name      string
		modelName string
		modality  string
		wantOK    bool
	}{
		{"empty modality always OK", "text-only-model", "", true},
		{"text modality always OK", "text-only-model", "text", true},
		{"image on text-only model fails", "text-only-model", "image", false},
		{"audio on text-only model fails", "text-only-model", "audio", false},
		{"video on text-only model fails", "text-only-model", "video", false},
		{"embedding on text-only model fails", "text-only-model", "embedding", false},
		{"image on image model succeeds", "image-model", "image", true},
		{"audio on audio model succeeds", "audio-model", "audio", true},
		{"image on full model succeeds", "full-model", "image", true},
		{"audio on full model succeeds", "full-model", "audio", true},
		{"video on full model succeeds", "full-model", "video", true},
		{"embedding on full model succeeds", "full-model", "embedding", true},
		{"unknown model always OK", "unknown-model", "image", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the validation logic from executor.validateOutputModality
			if tt.modality == "" || tt.modality == "text" {
				if !tt.wantOK {
					t.Fatal("empty/text modality should always succeed")
				}
				return
			}
			modelInfo := cfg.Lookup(tt.modelName)
			if modelInfo == nil {
				// Unknown model — allow
				if !tt.wantOK {
					t.Fatal("unknown model should always succeed")
				}
				return
			}
			got := modelInfo.SupportsOutputModality(tt.modality)
			if got != tt.wantOK {
				t.Errorf("model %q supports %q = %v, want %v", tt.modelName, tt.modality, got, tt.wantOK)
			}
		})
	}
}
