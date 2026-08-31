package input

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestLoadAskContentPartRejectsInvalidAttachmentClasses(t *testing.T) {
	dir := t.TempDir()
	directory := filepath.Join(dir, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	unsupported := filepath.Join(dir, "opaque.bin")
	if err := os.WriteFile(unsupported, []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		path   string
		reason string
	}{
		{name: "missing", path: filepath.Join(dir, "missing.txt"), reason: AttachmentReasonMissing},
		{name: "directory", path: directory, reason: AttachmentReasonNotRegular},
		{name: "unsupported content", path: unsupported, reason: AttachmentReasonUnsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadAskContentPart(tc.path)
			if err == nil {
				t.Fatal("LoadAskContentPart() error = nil, want rejection")
			}
			var attachmentErr *AttachmentError
			if !errors.As(err, &attachmentErr) {
				t.Fatalf("error = %v, want *AttachmentError", err)
			}
			if attachmentErr.Path != tc.path || attachmentErr.Reason == "" || !strings.Contains(attachmentErr.Reason, tc.reason) {
				t.Fatalf("attachment error = %#v, want path %q and reason containing %q", attachmentErr, tc.path, tc.reason)
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Errorf("error = %q, want supplied path", err)
			}
		})
	}
}

func TestLoadAskContentPartRejectsUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode-bit unreadability is not portable to Windows")
	}

	path := filepath.Join(t.TempDir(), "unreadable.txt")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600)

	_, err := LoadAskContentPart(path)
	if err == nil {
		t.Fatal("LoadAskContentPart() error = nil, want unreadable attachment rejection")
	}
	var attachmentErr *AttachmentError
	if !errors.As(err, &attachmentErr) || attachmentErr.Path != path || !strings.Contains(attachmentErr.Reason, AttachmentReasonUnreadable) {
		t.Fatalf("error = %v, want unreadable attachment for %q", err, path)
	}
}

func TestLoadAskContentPartsReturnsNoPartialSet(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "notes.txt")
	invalid := filepath.Join(dir, "opaque.bin")
	if err := os.WriteFile(valid, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalid, []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}

	parts, err := LoadAskContentParts([]string{valid, invalid})
	if err == nil {
		t.Fatal("LoadAskContentParts() error = nil, want rejection")
	}
	if parts != nil {
		t.Fatalf("parts = %#v, want nil on any invalid attachment", parts)
	}
}

func TestLoadAskContentPartsPreservesOnePartPerValidAttachment(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "photo.png")
	textPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(imagePath, []byte("fake png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(textPath, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	parts, err := LoadAskContentParts([]string{imagePath, textPath})
	if err != nil {
		t.Fatalf("LoadAskContentParts() error = %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %#v, want one part per file", parts)
	}
	imagePart, ok := parts[0].(messages.ImagePart)
	if !ok || imagePart.MediaType != "image/png" {
		t.Fatalf("first part = %#v, want image/png ImagePart", parts[0])
	}
	filePart, ok := parts[1].(messages.FilePart)
	if !ok || filePart.MediaType != "text/plain" || filePart.Name != "notes.txt" {
		t.Fatalf("second part = %#v, want text/plain notes.txt FilePart", parts[1])
	}
}
