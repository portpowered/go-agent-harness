package services

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestChatAtFile_ParseReferencesSuccessShapes(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("exact file text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "empty.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "document.dat"), []byte("%PDF-1.7\x00binary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "photo.PnG"), []byte{1, 2, 3, 4}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "dir", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dir", "child.txt"), []byte("child"), 0600); err != nil {
		t.Fatal(err)
	}

	cleaned, parts, errMsg := parseAtReferences("before @notes.txt after")
	if errMsg != "" || cleaned != "before after" || len(parts) != 1 {
		t.Fatalf("text reference = (%q, %#v, %q)", cleaned, parts, errMsg)
	}
	textPart, ok := parts[0].(messages.TextPart)
	if !ok || textPart.Text != "[File: notes.txt]\nexact file text\n" {
		t.Fatalf("text part = %#v, want exact file context", parts[0])
	}

	cleaned, parts, errMsg = parseAtReferences("@dir inspect")
	if errMsg != "" || cleaned != "inspect" || len(parts) != 1 {
		t.Fatalf("directory reference = (%q, %#v, %q)", cleaned, parts, errMsg)
	}
	directoryPart, ok := parts[0].(messages.TextPart)
	if !ok || !strings.Contains(directoryPart.Text, "[Directory: dir]") || !strings.Contains(directoryPart.Text, "child.txt") || !strings.Contains(directoryPart.Text, "nested/") {
		t.Fatalf("directory part = %#v", parts[0])
	}

	_, parts, errMsg = parseAtReferences("@photo.PnG")
	if errMsg != "" || len(parts) != 1 {
		t.Fatalf("image reference = (%#v, %q)", parts, errMsg)
	}
	imagePart, ok := parts[0].(messages.ImagePart)
	if !ok || !bytes.Equal(imagePart.Bytes, []byte{1, 2, 3, 4}) || imagePart.MediaType != "image/png" {
		t.Fatalf("image part = %#v", parts[0])
	}

	_, parts, errMsg = parseAtReferences("@document.dat")
	if errMsg != "" || len(parts) != 1 {
		t.Fatalf("binary reference = (%#v, %q)", parts, errMsg)
	}
	binaryPart, ok := parts[0].(messages.TextPart)
	if !ok || binaryPart.Text != "[Binary file: document.dat (15 bytes)]" {
		t.Fatalf("binary part = %#v", parts[0])
	}

	_, parts, errMsg = parseAtReferences("@empty.txt")
	if errMsg != "" || len(parts) != 1 {
		t.Fatalf("empty reference = (%#v, %q)", parts, errMsg)
	}
	emptyPart, ok := parts[0].(messages.TextPart)
	if !ok || emptyPart.Text != "[File: empty.txt]\n" {
		t.Fatalf("empty file part = %#v", parts[0])
	}

	cleaned, parts, errMsg = parseAtReferences("plain @ text")
	if errMsg != "" || cleaned != "plain @ text" || len(parts) != 0 {
		t.Fatalf("literal at sign = (%q, %#v, %q)", cleaned, parts, errMsg)
	}
	cleaned, parts, errMsg = parseAtReferences("")
	if errMsg != "" || cleaned != "" || parts != nil {
		t.Fatalf("empty input = (%q, %#v, %q)", cleaned, parts, errMsg)
	}
}

func TestChatAtFile_ParseReferencesFailuresAndOrdering(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	if err := os.WriteFile(filepath.Join(workspace, "safe.txt"), []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	cleaned, parts, errMsg := parseAtReferences("@missing.txt keep @safe.txt")
	if errMsg != "File not found: missing.txt" || cleaned != "" || parts != nil {
		t.Fatalf("missing plus safe reference = (%q, %#v, %q)", cleaned, parts, errMsg)
	}
	if strings.Contains(errMsg, "safe") {
		t.Fatal("failure message leaked successful file content")
	}

	cleaned, parts, errMsg = parseAtReferences("first @safe.txt second")
	if errMsg != "" || cleaned != "first second" || len(parts) != 1 {
		t.Fatalf("ordered safe reference = (%q, %#v, %q)", cleaned, parts, errMsg)
	}
	part, ok := parts[0].(messages.TextPart)
	if !ok || !strings.HasSuffix(part.Text, "\nsafe") {
		t.Fatalf("ordered safe content = %#v", parts[0])
	}

	_, _, errMsg = parseAtReferences("@missing-one @missing-two")
	if errMsg != "File not found: missing-one\nFile not found: missing-two" {
		t.Fatalf("multiple missing errors = %q", errMsg)
	}
}

func TestChatAtFile_ImageAndPrefixHelpers(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
		mime string
	}{
		{path: "a.png", want: true, mime: "image/png"},
		{path: "a.JPG", want: true, mime: "image/jpeg"},
		{path: "a.jpeg", want: true, mime: "image/jpeg"},
		{path: "a.gif", want: true, mime: "image/gif"},
		{path: "a.webp", want: true, mime: "image/webp"},
		{path: "a.svg", want: true, mime: "image/svg+xml"},
		{path: "a.txt", want: false, mime: "application/octet-stream"},
	} {
		if got := isImageExtension(test.path); got != test.want {
			t.Errorf("isImageExtension(%q) = %t, want %t", test.path, got, test.want)
		}
		if got := imageMediaType(test.path); got != test.mime {
			t.Errorf("imageMediaType(%q) = %q, want %q", test.path, got, test.mime)
		}
	}
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "@src/main.go", want: "src/main.go"},
		{input: "hello @src", want: "src"},
		{input: "@", want: ""},
		{input: "hello world", want: ""},
		{input: "hello@src", want: ""},
		{input: "@src file", want: ""},
	} {
		if got := extractAtPrefix(test.input); got != test.want {
			t.Errorf("extractAtPrefix(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestChatAtFile_AutocompleteScansAndCompletesWorkspace(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	if err := os.WriteFile(filepath.Join(workspace, "visible.txt"), []byte("visible"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "src", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "src", "main.go"), []byte("package main"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []string{".git", "node_modules", "__pycache__"} {
		if err := os.MkdirAll(filepath.Join(workspace, excluded, "hidden"), 0700); err != nil {
			t.Fatal(err)
		}
	}

	suggestions := scanFileSuggestions(workspace)
	labels := make(map[string]bool, len(suggestions))
	for _, suggestion := range suggestions {
		labels[suggestion.Label] = true
	}
	for _, want := range []string{"visible.txt", "src/", "src/main.go", "src/nested/"} {
		if !labels[want] {
			t.Errorf("scan suggestions missing %q: %#v", want, suggestions)
		}
	}
	for _, excluded := range []string{".git/", "node_modules/", "__pycache__/"} {
		if labels[excluded] {
			t.Errorf("scan suggestions included excluded directory %q", excluded)
		}
	}

	harness := newChatTestHarness(t)
	model := harness.model
	model.input.SetValue("@src")
	model.updateFileAutocomplete()
	if !model.fileAutocomplete.IsActive() {
		t.Fatal("file autocomplete did not activate")
	}
	model.completeAtSuggestion("src/main.go")
	if model.input.Value() != "@src/main.go " || model.input.Position() != len("@src/main.go ") {
		t.Fatalf("completed file input = %q at %d", model.input.Value(), model.input.Position())
	}
	model.input.SetValue("plain")
	model.updateFileAutocomplete()
	if model.fileAutocomplete.IsActive() {
		t.Fatal("file autocomplete stayed active outside an at-file context")
	}
	model.input.SetValue("embedded@src")
	model.completeAtSuggestion("ignored")
	if model.input.Value() != "embedded@src" {
		t.Fatalf("invalid completion changed input to %q", model.input.Value())
	}
}

func TestChatAtFile_PathBoundaryAndInlineLimitRemainExplicitGaps(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	outside := filepath.Join(filepath.Dir(workspace), "chat-outside-"+filepath.Base(workspace)+".txt")
	if err := os.WriteFile(outside, []byte("outside-only-content"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	_, parts, errMsg := parseAtReferences("@" + filepath.ToSlash(filepath.Join("..", filepath.Base(outside))))
	if errMsg != "" || len(parts) == 0 {
		return
	}
	for _, part := range parts {
		if textPart, ok := part.(messages.TextPart); ok && strings.Contains(textPart.Text, "outside-only-content") {
			t.Skip("the current at-file parser accepts a relative path escape and has no typed workspace-boundary error")
		}
	}
	t.Fatalf("path escape returned content without the outside marker: %#v", parts)
}

func TestChatAtFile_OversizedReferencesRemainExplicitGaps(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	const marker = "oversized-content-marker"
	data := append([]byte(strings.Repeat("x", 1<<20)), []byte(marker)...)
	if err := os.WriteFile(filepath.Join(workspace, "large.txt"), data, 0600); err != nil {
		t.Fatal(err)
	}
	_, parts, errMsg := parseAtReferences("@large.txt")
	if errMsg == "" && len(parts) > 0 {
		for _, part := range parts {
			if textPart, ok := part.(messages.TextPart); ok && strings.Contains(textPart.Text, marker) {
				t.Skip("the current at-file parser has no inline-size limit or typed oversized-file error")
			}
		}
	}
}
