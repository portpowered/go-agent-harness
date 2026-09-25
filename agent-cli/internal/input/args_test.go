package input

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestParseAskArgs_AttachmentBeforePromptKeepsPrompt(t *testing.T) {
	video := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(video, []byte("mp4 content"), 0o600); err != nil {
		t.Fatal(err)
	}

	prompt, paths := ParseAskArgs([]string{video, "is that a fish or a banana?"})
	if prompt != "is that a fish or a banana?" {
		t.Fatalf("prompt = %q, want the prompt that follows the attachment", prompt)
	}
	if !reflect.DeepEqual(paths, []string{video}) {
		t.Fatalf("attachment paths = %#v, want %q", paths, video)
	}
}

func TestLoadAskContentPart_MP4BecomesVideoPart(t *testing.T) {
	video := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(video, []byte("mp4 content"), 0o600); err != nil {
		t.Fatal(err)
	}

	part, err := LoadAskContentPart(video)
	if err != nil {
		t.Fatalf("load .mp4 attachment: %v", err)
	}
	videoPart, ok := part.(messages.VideoPart)
	if !ok {
		t.Fatalf("content part = %T, want messages.VideoPart", part)
	}
	if string(videoPart.Bytes) != "mp4 content" || videoPart.MediaType != "video/mp4" {
		t.Fatalf("video part = {%q, %q}, want the file bytes as video/mp4", videoPart.Bytes, videoPart.MediaType)
	}
}

func TestParseAskArgs_PreservesPromptAndAttachmentIntent(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "notes.txt")
	missing := filepath.Join(dir, "missing.txt")
	if err := os.WriteFile(valid, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	prompt, paths := ParseAskArgs([]string{"summarize this file", valid, missing, dir})
	if prompt != "summarize this file" {
		t.Fatalf("prompt = %q, want quoted prompt preserved", prompt)
	}
	wantPaths := []string{valid, missing, dir}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("attachment paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestParseAskArgs_LeavesOrdinaryPromptTextAlone(t *testing.T) {
	prompt, paths := ParseAskArgs([]string{"what", "is", "the", "answer?"})
	if prompt != "what is the answer?" {
		t.Fatalf("prompt = %q, want ordinary text", prompt)
	}
	if len(paths) != 0 {
		t.Fatalf("attachment paths = %#v, want none", paths)
	}
}

func TestParseAskArgs_RecognizesMissingSimpleFilename(t *testing.T) {
	const missing = "missing-attachment.png"
	prompt, paths := ParseAskArgs([]string{"describe this", missing})
	if prompt != "describe this" {
		t.Fatalf("prompt = %q, want attachment removed", prompt)
	}
	if !reflect.DeepEqual(paths, []string{missing}) {
		t.Fatalf("attachment paths = %#v, want %q", paths, missing)
	}
}

func TestParseAskArgs_RecognizesExistingPathWithSpaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attachment with spaces.txt")
	if err := os.WriteFile(path, []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	prompt, paths := ParseAskArgs([]string{"summarize", path})
	if prompt != "summarize" {
		t.Fatalf("prompt = %q, want prompt without path", prompt)
	}
	if !reflect.DeepEqual(paths, []string{path}) {
		t.Fatalf("attachment paths = %#v, want %q", paths, path)
	}
}

func TestParseAskArgs_RecognizesQuotedLeadingTildePathWithSpaces(t *testing.T) {
	prompt, paths := ParseAskArgs([]string{"summarize", "~/notes with spaces.txt"})
	if prompt != "summarize" {
		t.Fatalf("prompt = %q, want prompt without tilde attachment", prompt)
	}
	if !reflect.DeepEqual(paths, []string{"~/notes with spaces.txt"}) {
		t.Fatalf("attachment paths = %#v, want quoted leading-tilde path", paths)
	}
}
