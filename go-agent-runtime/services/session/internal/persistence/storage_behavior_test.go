package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestStorage_FilesystemSandbox(t *testing.T) {
	workspace := t.TempDir()
	st := NewStorage(workspace)
	if got := st.WorkspaceDir(); got != workspace {
		t.Fatalf("WorkspaceDir: got %q, want %q", got, workspace)
	}

	want := completeStoredMessages()
	const roundTripID = "round-trip"
	if err := st.Save(roundTripID, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	sessionsDir := filepath.Join(workspace, sessionDirName)
	if info, err := os.Stat(sessionsDir); err != nil {
		t.Fatalf("stat sessions directory: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("sessions path is not a directory: %s", sessionsDir)
	}

	path := st.sessionPath(roundTripID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved session: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("saved session is not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved session: %v", err)
	}
	if len(data) == 0 || !json.Valid(data) {
		t.Fatalf("saved session is not non-empty valid JSON: %q", data)
	}
	var stored StoredSession
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("unmarshal saved session: %v", err)
	}
	if stored.ID != roundTripID {
		t.Fatalf("stored ID: got %q, want %q", stored.ID, roundTripID)
	}
	if len(stored.Messages) == 0 {
		t.Fatal("saved session has no messages")
	}

	got, err := st.Load(roundTripID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip messages differ:\n got: %#v\nwant: %#v", got, want)
	}

	checkInlineBytePayload(t, st)
	checkStorageListingAndDelete(t, st)
	checkMissingStorage(t, workspace)
	checkUnsupportedContent(t, st)
}

func checkInlineBytePayload(t *testing.T, st *Storage) {
	t.Helper()
	t.Run("inline_byte_payload_contract", func(t *testing.T) {
		byteMessages := []messages.Message{{
			Role: messages.RoleUser,
			ContentParts: []messages.ContentPart{
				messages.ImagePart{MediaType: "image/png", Bytes: []byte{0x00, 0x01, 0x02}},
				messages.AudioPart{MediaType: "audio/wav", Bytes: []byte{0x03, 0x04}},
				messages.VideoPart{MediaType: "video/mp4", Bytes: []byte{0x05, 0x06}},
				messages.FilePart{MediaType: "application/pdf", Name: "inline.pdf", Bytes: []byte{0x07, 0x08}},
			},
			ToolCalls: []messages.ToolCall{},
		}}
		const byteID = "inline-bytes"
		if err := st.Save(byteID, byteMessages); err != nil {
			t.Fatalf("Save inline bytes: %v", err)
		}
		loaded, err := st.Load(byteID)
		if err != nil {
			t.Fatalf("Load inline bytes: %v", err)
		}
		if !reflect.DeepEqual(loaded, byteMessages) {
			t.Fatalf("inline media changed after storage round trip: got %#v, want %#v", loaded, byteMessages)
		}
	})

}

func checkStorageListingAndDelete(t *testing.T, st *Storage) {
	t.Helper()
	const roundTripID = "round-trip"
	sessionsDir := st.sessionsDir
	old := []messages.Message{messages.NewTextMessage(messages.RoleUser, "old")}
	old[0].ToolCalls = []messages.ToolCall{}
	newest := []messages.Message{{
		Role:         messages.RoleAssistant,
		ContentParts: []messages.ContentPart{messages.TextPart{Text: "newest"}},
		ToolCalls:    []messages.ToolCall{},
	}}
	sanitized := []messages.Message{{
		Role:         messages.RoleTool,
		ContentParts: []messages.ContentPart{messages.TextPart{Text: "sanitized"}},
		ToolCalls:    []messages.ToolCall{},
	}}
	for id, msgs := range map[string][]messages.Message{
		"oldest": old,
		"newest": newest,
		"unsafe" + string(filepath.Separator) + "id": sanitized,
	} {
		if err := st.Save(id, msgs); err != nil {
			t.Fatalf("Save %q: %v", id, err)
		}
	}

	sanitizedID := "unsafe" + string(filepath.Separator) + "id"
	sanitizedPath := st.sessionPath(sanitizedID)
	if got := filepath.Base(sanitizedPath); got != "session-unsafe-id.json" {
		t.Fatalf("sanitized filename: got %q, want %q", got, "session-unsafe-id.json")
	}
	if filepath.Dir(sanitizedPath) != sessionsDir {
		t.Fatalf("sanitized path escaped sessions directory: %s", sanitizedPath)
	}
	if loaded, err := st.Load(sanitizedID); err != nil {
		t.Fatalf("Load sanitized ID: %v", err)
	} else if !reflect.DeepEqual(loaded, sanitized) {
		t.Fatalf("sanitized round-trip: got %#v, want %#v", loaded, sanitized)
	}

	base := time.Unix(1_700_000_000, 0)
	setSessionModTime(t, st.sessionPath(roundTripID), base.Add(2*time.Minute))
	setSessionModTime(t, st.sessionPath("oldest"), base)
	setSessionModTime(t, st.sessionPath("newest"), base.Add(4*time.Minute))
	setSessionModTime(t, sanitizedPath, base.Add(3*time.Minute))
	setSessionModTime(t, st.sessionPath("inline-bytes"), base.Add(time.Minute))

	if err := os.WriteFile(filepath.Join(sessionsDir, "session-.json"), []byte("ignored"), 0600); err != nil {
		t.Fatalf("write empty-ID noise file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "not-a-session.json"), []byte("ignored"), 0600); err != nil {
		t.Fatalf("write unrelated noise file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(sessionsDir, "session-directory.json"), 0700); err != nil {
		t.Fatalf("create directory noise entry: %v", err)
	}

	infos, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantIDs := []string{"newest", "unsafe-id", roundTripID, "inline-bytes", "oldest"}
	if gotIDs := sessionInfoIDs(infos); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("List IDs: got %#v, want %#v", gotIDs, wantIDs)
	}
	if len(infos) != len(wantIDs) {
		t.Fatalf("List count: got %d, want %d", len(infos), len(wantIDs))
	}
	if latest, err := st.Latest(); err != nil {
		t.Fatalf("Latest: %v", err)
	} else if latest != "newest" {
		t.Fatalf("Latest: got %q, want %q", latest, "newest")
	}

	checkStorageDeletion(t, st, sanitizedID)
}

func checkStorageDeletion(t *testing.T, st *Storage, sanitizedID string) {
	t.Helper()
	const roundTripID = "round-trip"
	sanitizedPath := st.sessionPath(sanitizedID)
	if err := st.Delete(sanitizedID); err != nil {
		t.Fatalf("Delete sanitized session: %v", err)
	}
	if _, err := os.Stat(sanitizedPath); !os.IsNotExist(err) {
		t.Fatalf("deleted session stat error: got %v, want not-exist", err)
	}
	if loaded, err := st.Load(sanitizedID); err != nil {
		t.Fatalf("Load deleted session: %v", err)
	} else if loaded != nil {
		t.Fatalf("Load deleted session: got %#v, want nil", loaded)
	}

	infos, err := st.List()
	if err != nil {
		t.Fatalf("List after Delete: %v", err)
	}
	wantIDs := []string{"newest", roundTripID, "inline-bytes", "oldest"}
	if gotIDs := sessionInfoIDs(infos); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("List IDs after Delete: got %#v, want %#v", gotIDs, wantIDs)
	}
	if latest, err := st.Latest(); err != nil {
		t.Fatalf("Latest after Delete: %v", err)
	} else if latest != "newest" {
		t.Fatalf("Latest after Delete: got %q, want %q", latest, "newest")
	}

}

func checkMissingStorage(t *testing.T, workspace string) {
	t.Helper()
	st := NewStorage(workspace)
	emptyRoot := filepath.Join(workspace, "not-created")
	emptyStorage := NewStorage(emptyRoot)
	if loaded, err := emptyStorage.Load("missing"); err != nil {
		t.Fatalf("Load missing session: %v", err)
	} else if loaded != nil {
		t.Fatalf("Load missing session: got %#v, want nil", loaded)
	}
	if infos, err := emptyStorage.List(); err != nil {
		t.Fatalf("List missing root: %v", err)
	} else if len(infos) != 0 {
		t.Fatalf("List missing root count: got %d, want 0", len(infos))
	}
	if latest, err := emptyStorage.Latest(); err != nil {
		t.Fatalf("Latest missing root: %v", err)
	} else if latest != "" {
		t.Fatalf("Latest missing root: got %q, want empty", latest)
	}

	if id := st.NewSessionID(); id == "" {
		t.Fatal("NewSessionID returned empty ID")
	}

}

func checkUnsupportedContent(t *testing.T, st *Storage) {
	t.Helper()
	t.Run("unsupported_content_part_placeholder", func(t *testing.T) {
		unsupportedID := "unsupported-content"
		input := []messages.Message{{
			Role:         messages.RoleSystem,
			ContentParts: []messages.ContentPart{messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypePing}},
			ToolCalls:    []messages.ToolCall{},
		}}
		if err := st.Save(unsupportedID, input); err != nil {
			t.Fatalf("Save unsupported content: %v", err)
		}
		loaded, err := st.Load(unsupportedID)
		if err != nil {
			t.Fatalf("Load unsupported content: %v", err)
		}
		wantPlaceholder := []messages.Message{{
			Role:         messages.RoleSystem,
			ContentParts: []messages.ContentPart{messages.TextPart{Text: ""}},
			ToolCalls:    []messages.ToolCall{},
		}}
		if !reflect.DeepEqual(loaded, wantPlaceholder) {
			t.Fatalf("unsupported content placeholder: got %#v, want %#v", loaded, wantPlaceholder)
		}
	})
}

func TestStorage_ConcurrentSameIDWriters(t *testing.T) {
	st := NewStorage(t.TempDir())
	const sessionID = "same-session"
	left := completeStoredMessages()
	right := completeStoredMessages()
	right[0].ContentParts[0] = messages.TextPart{Text: "the other complete winner"}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, input := range [][]messages.Message{left, right} {
		wg.Add(1)
		go func(msgs []messages.Message) {
			defer wg.Done()
			<-start
			results <- st.Save(sessionID, msgs)
		}(input)
	}
	close(start)
	wg.Wait()
	close(results)

	var writeErrors []error
	for err := range results {
		if err == nil {
			continue
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("concurrent Save error: got %T %v, want wrapped *os.PathError", err, err)
		}
		writeErrors = append(writeErrors, err)
	}

	loaded, err := st.Load(sessionID)
	if err != nil {
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			t.Fatalf("concurrent Save produced corrupt JSON: %v", err)
		}
		if len(writeErrors) > 0 {
			var pathErr *os.PathError
			if !errors.As(err, &pathErr) {
				t.Fatalf("Load after typed concurrent write error: got %T %v, want wrapped *os.PathError", err, err)
			}
			return
		}
		t.Fatalf("Load concurrent winner: %v", err)
	}
	if loaded == nil {
		if len(writeErrors) > 0 {
			return
		}
		t.Fatal("Load concurrent winner: got nil without a typed write error")
	}
	if !reflect.DeepEqual(loaded, left) && !reflect.DeepEqual(loaded, right) {
		t.Fatalf("concurrent write produced a partial or merged session: %#v", loaded)
	}
}

func completeStoredMessages() []messages.Message {
	return []messages.Message{
		{
			Role: messages.RoleSystem,
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: "system context"},
				messages.ImagePart{MediaType: "image/png", URL: "https://example.test/image.png"},
				messages.AudioPart{MediaType: "audio/mpeg", URL: "https://example.test/audio.mp3"},
				messages.VideoPart{MediaType: "video/mp4", URL: "https://example.test/video.mp4"},
				messages.FilePart{MediaType: "application/pdf", URL: "https://example.test/file.pdf", Name: "brief.pdf"},
				messages.ReasoningPart{Reasoning: "because the input is complete"},
				messages.UsageInfoPart{Usage: messages.TokenUsage{
					PromptTokens: 11, CompletionTokens: 13, TotalTokens: 24, ReasoningTokens: 5,
				}},
			},
			ToolCalls: []messages.ToolCall{},
		},
		{
			Role:         messages.RoleAssistant,
			ContentParts: []messages.ContentPart{messages.TextPart{Text: "I will call the tool."}},
			ToolCalls: []messages.ToolCall{{
				ID: "call-123", Name: "lookup", Arguments: `{"city":"Paris","units":"metric"}`,
			}},
		},
		{
			Role:         messages.RoleTool,
			ContentParts: []messages.ContentPart{messages.TextPart{Text: "tool result"}},
			ToolCallID:   "call-123",
			Name:         "lookup",
			ToolCalls:    []messages.ToolCall{},
		},
	}
}

func setSessionModTime(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("Chtimes %s: %v", path, err)
	}
}

func sessionInfoIDs(infos []SessionInfo) []string {
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	return ids
}

func storageWithFileRoot(t *testing.T) *Storage {
	t.Helper()
	st := NewStorage(t.TempDir())
	if err := os.WriteFile(st.sessionsDir, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("write sessions file: %v", err)
	}
	return st
}

func storageWithInvalidSessionsPath(t *testing.T) *Storage {
	t.Helper()
	st := NewStorage(t.TempDir())
	st.sessionsDir += string(rune(0))
	return st
}

func writeRawSession(t *testing.T, st *Storage, id, data string) {
	t.Helper()
	if err := os.MkdirAll(st.sessionsDir, 0755); err != nil {
		t.Fatalf("MkdirAll sessions: %v", err)
	}
	if err := os.WriteFile(st.sessionPath(id), []byte(data), 0600); err != nil {
		t.Fatalf("write raw session %q: %v", id, err)
	}
}

func requirePathError(t *testing.T, err error, messagePrefix string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want *os.PathError with prefix %q", messagePrefix)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("error type: got %T %v, want wrapped *os.PathError", err, err)
	}
	if !strings.HasPrefix(err.Error(), messagePrefix) {
		t.Fatalf("error message: got %q, want prefix %q", err.Error(), messagePrefix)
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
