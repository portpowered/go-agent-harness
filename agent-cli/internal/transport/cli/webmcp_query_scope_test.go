package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type queryScopeCase struct {
	name   string
	tool   webmcp.ToolDescriptor
	input  string
	output string
}

// TestWebMCPQueryToolScopeUsesFreshnessGuard keeps the Margin read surface in
// one behavioral matrix. Every read-only page descriptor takes both caller
// paths and must preserve the same non-empty page payload; this is deliberately
// based on the shared invocation/result contract rather than source topology.
func TestWebMCPQueryToolScopeUsesFreshnessGuard(t *testing.T) {
	for _, testCase := range webmcpQueryScopeCases() {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newQueryParityFixtureWithTool(t, testCase.tool)
			live := fixture.runLiveQueryWithInput(t, "live-"+testCase.name, testCase.input, testCase.output)
			direct := fixture.runDirectQueryWithInput(t, "direct-"+testCase.name, testCase.input, testCase.output)

			liveOutput := decodeLiveQueryOutput(t, live, fixture.ref)
			directOutput := decodeDirectQueryOutput(t, direct, fixture.ref)
			assertNonEmptyScopePayload(t, testCase.name, liveOutput, []byte(testCase.output))
			assertNonEmptyScopePayload(t, testCase.name, directOutput, []byte(testCase.output))
			if !jsonEqual(liveOutput, directOutput) {
				t.Fatalf("live/direct decoded %s payloads differ: live=%s direct=%s", testCase.name, liveOutput, directOutput)
			}
			fixture.assertUnchangedSelection(t)
			fixture.assertOneTerminalPerInvocation(t, 2)
		})
	}
}

func webmcpQueryScopeCases() []queryScopeCase {
	return []queryScopeCase{
		{
			name: "get_document",
			tool: queryScopeReadTool(
				"get_document",
				"Read one document from the current Margin page.",
				`{"type":"object","properties":{"document_id":{"type":"string"}},"required":["document_id"],"additionalProperties":false}`,
			),
			input:  `{"document_id":"welcome-to-margin"}`,
			output: `{"id":"welcome-to-margin","title":"Welcome to Margin","content":"A welcome document for the Margin fixture."}`,
		},
		{
			name: "list_documents",
			tool: queryScopeReadTool(
				"list_documents",
				"List documents in the current Margin page.",
				`{"type":"object","properties":{},"additionalProperties":false}`,
			),
			input:  `{}`,
			output: `{"count":1,"documents":[{"id":"welcome-to-margin","title":"Welcome to Margin"}]}`,
		},
		{
			name: "list_comments",
			tool: queryScopeReadTool(
				"list_comments",
				"List comments for a document on the current Margin page.",
				`{"type":"object","properties":{"document_id":{"type":"string"}},"required":["document_id"],"additionalProperties":false}`,
			),
			input:  `{"document_id":"welcome-to-margin"}`,
			output: `{"count":1,"comments":[{"id":"comment-1","document_id":"welcome-to-margin","body":"Looks good."}]}`,
		},
	}
}

func queryScopeReadTool(name, description, inputSchema string) webmcp.ToolDescriptor {
	readOnly := true
	return webmcp.ToolDescriptor{
		Name:        name,
		Description: description,
		InputSchema: json.RawMessage(inputSchema),
		Annotations: webmcp.ToolAnnotations{ReadOnly: &readOnly},
		FrameID:     queryParityFrame,
	}
}

func assertNonEmptyScopePayload(t *testing.T, toolName string, got, want []byte) {
	t.Helper()
	if !json.Valid(got) || jsonEqual(got, []byte("null")) || jsonEqual(got, []byte(`{"count":0,"documents":[]}`)) {
		t.Fatalf("%s returned empty or invalid decoded payload: %s", toolName, got)
	}
	if !jsonEqual(got, want) {
		t.Fatalf("%s decoded payload = %s, want %s", toolName, got, want)
	}
}

func TestReadXVideo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mp4")
	data := []byte("\x00\x00\x00\x18ftypisomfixture")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, hash, err := readXVideo(path)
	sum := sha256.Sum256(data)
	if err != nil || string(got) != string(data) || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("read = %q %s %v", got, hash, err)
	}
	for _, bad := range []string{filepath.Dir(path), path + ".mov", path + "missing.mp4"} {
		if _, _, err := readXVideo(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if err := os.WriteFile(path, []byte("not a video at all"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readXVideo(path); err == nil {
		t.Fatal("accepted bad MP4 header")
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(xVideoMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readXVideo(path); err == nil {
		t.Fatal("accepted oversized MP4")
	}
}

func TestDecodeXVideoReply(t *testing.T) {
	for _, input := range []string{`null`, `{}`, `{"ok":false,"error":{"code":"hash_mismatch","message":"mismatch"}}`, `broken`} {
		if _, err := decodeXVideoReply([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	reply, err := decodeXVideoReply([]byte(`{"ok":true,"data":{"video_processing":true}}`))
	if err != nil || !reply.Data.VideoProcessing {
		t.Fatalf("reply=%+v error=%v", reply, err)
	}
}

type focusBridgeFixture struct {
	webmcp.TargetSession
	acquired, released int
}

func TestRestoreXVideoFocusAfterCancellation(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "kept"))
	cancel()
	want := errors.New("restore failed")
	err := restoreXVideoFocus(ctx, func(cleanup context.Context) error {
		if cleanup.Err() != nil || cleanup.Value(contextKey{}) != "kept" {
			t.Fatalf("cleanup lost lineage or inherited cancellation: %v", cleanup.Err())
		}
		if _, ok := cleanup.Deadline(); !ok {
			t.Fatal("cleanup must remain bounded")
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("cleanup error lost: %v", err)
	}
}

func (s *focusBridgeFixture) AcquirePageFocus(context.Context) (func(context.Context) error, error) {
	s.acquired++
	return func(context.Context) error { s.released++; return nil }, nil
}
func TestProductionSessionForwardsFocusLeaseToExactRawTarget(t *testing.T) {
	raw := &focusBridgeFixture{}
	session := &productionTargetSession{raw: raw}
	release, err := session.AcquirePageFocus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	session.raw = &focusBridgeFixture{} // cleanup must not look up the new target
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if raw.acquired != 1 || raw.released != 1 {
		t.Fatalf("raw=%+v", raw)
	}
}
