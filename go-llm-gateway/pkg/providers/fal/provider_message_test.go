package fal

import (
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestExtractImageAndTextFromMessages_URLImage(t *testing.T) {
	msgs := []models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "Generate a video from this image"},
			models.ImagePart{URL: "https://example.com/photo.png"},
		},
	}}
	imageURL, text, err := extractImageAndTextFromMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if imageURL != "https://example.com/photo.png" {
		t.Errorf("imageURL = %q, want https://example.com/photo.png", imageURL)
	}
	if text != "Generate a video from this image" {
		t.Errorf("text = %q, want %q", text, "Generate a video from this image")
	}
}

func TestExtractImageAndTextFromMessages_InlineBytesDataURI(t *testing.T) {
	msgs := []models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.ImagePart{Bytes: []byte("png-bytes"), MediaType: "image/png"},
		},
	}}
	imageURL, _, err := extractImageAndTextFromMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Errorf("imageURL should be data URI, got %q", imageURL)
	}
}

func TestExtractImageAndTextFromMessages_InlineBytesDefaultMediaType(t *testing.T) {
	msgs := []models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.ImagePart{Bytes: []byte("raw-bytes")},
		},
	}}
	imageURL, _, err := extractImageAndTextFromMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Errorf("imageURL should default to image/png, got %q", imageURL)
	}
}

func TestExtractImageAndTextFromMessages_TextOnly(t *testing.T) {
	msgs := []models.Message{{
		Role:         models.RoleUser,
		ContentParts: []models.ContentPart{models.TextPart{Text: "just text"}},
	}}
	imageURL, text, err := extractImageAndTextFromMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if imageURL != "" {
		t.Errorf("imageURL = %q, want empty", imageURL)
	}
	if text != "just text" {
		t.Errorf("text = %q, want %q", text, "just text")
	}
}

func TestExtractImageAndTextFromMessages_MixedImageAndText(t *testing.T) {
	msgs := []models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.TextPart{Text: "first"},
			models.ImagePart{URL: "https://example.com/img.jpg"},
			models.TextPart{Text: "second"},
		},
	}}
	imageURL, text, err := extractImageAndTextFromMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if imageURL != "https://example.com/img.jpg" {
		t.Errorf("imageURL = %q, want https://example.com/img.jpg", imageURL)
	}
	if text != "first second" {
		t.Errorf("text = %q, want %q", text, "first second")
	}
}

func TestExtractImageAndTextFromMessages_NoUserMessage(t *testing.T) {
	msgs := []models.Message{
		models.NewTextMessage(models.RoleAssistant, "ok"),
	}
	_, _, err := extractImageAndTextFromMessages(msgs)
	if err == nil {
		t.Fatal("expected error for no user message, got nil")
	}
	if !strings.Contains(err.Error(), "no user message with image or text found") {
		t.Errorf("error = %v, want substring about no user message", err)
	}
}

func TestExtractImageAndTextFromMessages_EmptyMessages(t *testing.T) {
	_, _, err := extractImageAndTextFromMessages(nil)
	if err == nil {
		t.Fatal("expected error for empty messages, got nil")
	}
}

func TestExtractImageAndTextFromMessages_LastUserMessage(t *testing.T) {
	msgs := []models.Message{
		{
			Role:         models.RoleUser,
			ContentParts: []models.ContentPart{models.ImagePart{URL: "https://example.com/old.png"}},
		},
		models.NewTextMessage(models.RoleAssistant, "ok"),
		{
			Role:         models.RoleUser,
			ContentParts: []models.ContentPart{models.ImagePart{URL: "https://example.com/new.png"}},
		},
	}
	imageURL, _, err := extractImageAndTextFromMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if imageURL != "https://example.com/new.png" {
		t.Errorf("imageURL = %q, want https://example.com/new.png (last user message)", imageURL)
	}
}

func TestExtractImageAndTextFromMessages_MultipleImages_TakesFirst(t *testing.T) {
	msgs := []models.Message{{
		Role: models.RoleUser,
		ContentParts: []models.ContentPart{
			models.ImagePart{URL: "https://example.com/first.png"},
			models.ImagePart{URL: "https://example.com/second.png"},
		},
	}}
	imageURL, _, err := extractImageAndTextFromMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if imageURL != "https://example.com/first.png" {
		t.Errorf("imageURL = %q, want first image URL", imageURL)
	}
}
