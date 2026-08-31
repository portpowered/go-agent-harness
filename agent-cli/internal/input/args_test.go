package input

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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
