package xvideo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/operations"
)

const (
	testAccount  = "@handle"
	testPageURL  = "https://x.com/home"
	testBrowser  = "browser-a"
	testTarget   = "target-1"
	testDraft    = "draft-1"
	testUpload   = "upload-1"
	toolRefStart = "webmcp.tool-ref.v1:"
)

func writeVideo(t *testing.T, size int) string {
	t.Helper()
	data := bytes.Repeat([]byte{'v'}, size)
	copy(data, "\x00\x00\x00\x18ftypisom")
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadVideo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mp4")
	data := []byte("\x00\x00\x00\x18ftypisomfixture")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, hash, err := ReadVideo(path)
	sum := sha256.Sum256(data)
	if err != nil || string(got) != string(data) || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("read = %q %s %v", got, hash, err)
	}
	for _, bad := range []string{filepath.Dir(path), path + ".mov", path + "missing.mp4"} {
		if _, _, err := ReadVideo(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if err := os.WriteFile(path, []byte("not a video at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadVideo(path); err == nil {
		t.Fatal("accepted bad MP4 header")
	}
	if err := os.Truncate(path, MaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadVideo(path); err == nil {
		t.Fatal("accepted oversized MP4")
	}
}

func TestDecodeReply(t *testing.T) {
	for _, input := range []string{`null`, `{}`, `{"ok":false,"error":{"code":"hash_mismatch","message":"mismatch"}}`, `broken`} {
		if _, err := DecodeReply([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	reply, err := DecodeReply([]byte(`{"ok":true,"data":{"video_processing":true}}`))
	if err != nil || !reply.Data.VideoProcessing {
		t.Fatalf("reply=%+v error=%v", reply, err)
	}
}

func TestRestoreFocusAfterCancellation(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "kept"))
	cancel()
	want := errors.New("restore failed")
	err := restoreFocus(ctx, func(cleanup context.Context) error {
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

// xBroker is a selected X page whose adapter tools acknowledge every chunk
// and finish processing on the first prepare call.
type xBroker struct {
	url       string
	received  int
	calls     []string
	released  int
	noToken   bool
	refuseAll bool
}

func (b *xBroker) Discover(context.Context, webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	return []webmcp.BrowserCandidate{{ID: testBrowser}}, nil
}

func (b *xBroker) ListTargets(context.Context, webmcp.BrowserSelector) ([]webmcp.Target, error) {
	return []webmcp.Target{{ID: testTarget, Type: "page", URL: b.url, Origin: "https://x.com", Eligible: true}}, nil
}

func (b *xBroker) Select(_ context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	return webmcp.PageContext{Key: webmcp.PageKey(selector), URL: b.url, Connected: true}, nil
}

func (b *xBroker) Selected(context.Context) (webmcp.PageContext, error) {
	return webmcp.PageContext{}, nil
}

func (b *xBroker) ListTools(context.Context, webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	tools := make([]webmcp.ToolDescriptor, 0)
	for _, name := range []string{toolBeginUpload, toolAppendChunk, toolPreparePost} {
		tools = append(tools, webmcp.ToolDescriptor{Ref: webmcp.ToolRef(toolRefStart + name), Name: name})
	}
	return webmcp.ToolCatalogSnapshot{Tools: tools}, nil
}

func (b *xBroker) Invoke(_ context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	name := strings.TrimPrefix(string(request.ToolRef), toolRefStart)
	b.calls = append(b.calls, name)
	var input map[string]any
	if err := json.Unmarshal(request.Input, &input); err != nil {
		return webmcp.InvokeResult{}, err
	}
	data := map[string]any{}
	switch name {
	case toolBeginUpload:
		if !b.noToken {
			data[keyUploadToken] = testUpload
		}
	case toolAppendChunk:
		encoded, isString := input["data_base64"].(string)
		chunk, err := base64.StdEncoding.DecodeString(encoded)
		if !isString || err != nil {
			return webmcp.InvokeResult{}, errors.New("chunk is not base64 text")
		}
		b.received += len(chunk)
		data["received_bytes"] = b.received
	case toolPreparePost:
		data["draft_token"] = testDraft
	}
	output, err := json.Marshal(map[string]any{"ok": !b.refuseAll, "data": data})
	return webmcp.InvokeResult{State: webmcp.InvocationCompleted, Output: output}, err
}

func (b *xBroker) Cancel(context.Context, webmcp.CancelRequest) error { return nil }

func (b *xBroker) Watch(context.Context) <-chan webmcp.BrokerEvent { return nil }

func (b *xBroker) Close() error { return nil }

func (b *xBroker) AcquirePageFocus(context.Context) (func(context.Context) error, error) {
	return func(context.Context) error { b.released++; return nil }, nil
}

func prepareRequest(t *testing.T, size int, recovery *bytes.Buffer) Request {
	return Request{
		Selector: operations.Selector{Browser: config.BrowserConfig{Selection: config.BrowserSelectionConfig{Tab: testTarget}}},
		File:     writeVideo(t, size),
		Caption:  " caption\r\ntext ",
		Account:  testAccount,
		Reason:   "test",
		Recovery: recovery,
	}
}

func TestPrepareTransfersAcknowledgedChunksAndRestoresFocus(t *testing.T) {
	broker := &xBroker{url: testPageURL}
	var recovery bytes.Buffer
	size := ChunkBytes + ChunkBytes/2
	result, err := Prepare(context.Background(), broker, prepareRequest(t, size, &recovery))
	if err != nil {
		t.Fatalf("Prepare = %v", err)
	}
	if !strings.Contains(string(result.Output), testDraft) || result.ToolRef != toolRefStart+toolPreparePost {
		t.Fatalf("result = %+v, want the prepared draft", result)
	}
	wantCalls := []string{toolBeginUpload, toolAppendChunk, toolAppendChunk, toolPreparePost}
	if strings.Join(broker.calls, ",") != strings.Join(wantCalls, ",") || broker.received != size || broker.released != 1 {
		t.Fatalf("calls %v received %d released %d", broker.calls, broker.received, broker.released)
	}
	var metadata map[string]any
	if err := json.Unmarshal(recovery.Bytes(), &metadata); err != nil || metadata[keyUploadToken] != testUpload || metadata["version"] != transferVersion {
		t.Fatalf("recovery metadata = %q (%v)", recovery.String(), err)
	}
}

func TestPrepareRefusesUnsafeRequests(t *testing.T) {
	cases := []struct {
		name   string
		broker *xBroker
		mutate func(*Request)
	}{
		{name: "account", broker: &xBroker{url: testPageURL}, mutate: func(r *Request) { r.Account = "handle" }},
		{name: "caption", broker: &xBroker{url: testPageURL}, mutate: func(r *Request) { r.Caption = " " }},
		{name: "file", broker: &xBroker{url: testPageURL}, mutate: func(r *Request) { r.File += ".mov" }},
		{name: "page", broker: &xBroker{url: "https://example.com/"}},
		{name: "refused", broker: &xBroker{url: testPageURL, refuseAll: true}},
		{name: "token", broker: &xBroker{url: testPageURL, noToken: true}},
		{name: "recovery", broker: &xBroker{url: testPageURL}, mutate: func(r *Request) { r.Recovery = nil }},
	}
	for _, testCase := range cases {
		request := prepareRequest(t, ChunkBytes/2, &bytes.Buffer{})
		if testCase.mutate != nil {
			testCase.mutate(&request)
		}
		if _, err := Prepare(context.Background(), testCase.broker, request); err == nil {
			t.Fatalf("%s: Prepare succeeded", testCase.name)
		}
		if strings.Contains(strings.Join(testCase.broker.calls, ","), toolPreparePost) {
			t.Fatalf("%s: prepared a draft after refusal: %v", testCase.name, testCase.broker.calls)
		}
	}
}
