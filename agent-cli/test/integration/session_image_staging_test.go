package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestSessionCommandImageAndScheduledAudioUsesExactStagedImagePath(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "fresh-config")
	imagePath, imageBytes := writeStagedImageFixture(t, root)
	audioPath := filepath.Join(root, "question.raw")
	writeStagedAudioFixture(t, audioPath)
	recordingDir := filepath.Join(root, "recording")

	session := newExactStagedImageSession(configDir, imageBytes)
	capability, err := runtimeToolsWire.NewService().Resolve(context.Background(), runtimeTools.Request{
		WorkDir:        root,
		UseDefaultTool: true,
	})
	if err != nil {
		t.Fatalf("resolve runtime tools: %v", err)
	}
	toolService := serviceTools.Factory(func(_ *config.Config) (serviceTools.Capabilities, error) {
		return serviceTools.Capabilities{
			Executor:    capability.Executor,
			Definitions: append([]messages.ToolDefinition(nil), capability.Definitions...),
		}, nil
	})
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewToolServicePort(toolService),
		wire.NewPortSwap(wire.PortInferencer, &mockInferencerError{err: errors.New("stateless inferencer must not be used")}),
		wire.NewPortSwap(wire.PortSessionInferencer, &exactStagedImageInferencer{session: session}),
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	var output bytes.Buffer
	rootCommand := agentCLI.Generate()
	rootCommand.SetOut(&output)
	rootCommand.SetErr(&output)
	rootCommand.SetArgs([]string{
		"--config-dir", configDir,
		"--workdir", root,
		"session",
		"--record-dir", recordingDir,
		"--provider", "openai",
		"--model", "gpt-realtime-2.1-mini",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--image", imagePath,
		"--audio-in-turn", audioPath,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rootCommand.ExecuteContext(ctx); err != nil {
		t.Fatalf("execute image/audio session: %v\noutput:\n%s", err, output.String())
	}

	snapshot := session.snapshot()
	if snapshot.failure != nil {
		t.Fatalf("scripted provider validation: %v", snapshot.failure)
	}
	if snapshot.advertisedPath == "" {
		t.Fatal("provider never received a staged read_image path")
	}
	cleanConfigDir, err := filepath.Abs(configDir)
	if err != nil {
		t.Fatalf("resolve config directory: %v", err)
	}
	cleanConfigDir = filepath.Clean(cleanConfigDir)
	if !filepath.IsAbs(snapshot.advertisedPath) || filepath.Clean(snapshot.advertisedPath) != snapshot.advertisedPath {
		t.Fatalf("advertised image path = %q, want a cleaned absolute path", snapshot.advertisedPath)
	}
	if !strings.HasPrefix(snapshot.advertisedPath, cleanConfigDir+string(os.PathSeparator)) {
		t.Fatalf("advertised image path = %q, want it under config directory %q", snapshot.advertisedPath, cleanConfigDir)
	}
	if strings.Count(snapshot.advertisedPath, cleanConfigDir) != 1 {
		t.Fatalf("advertised image path = %q, want config directory exactly once", snapshot.advertisedPath)
	}
	if snapshot.toolCallPath != snapshot.advertisedPath {
		t.Fatalf("tool call path = %q, want exact advertised path %q", snapshot.toolCallPath, snapshot.advertisedPath)
	}
	if !snapshot.stagedReadable {
		t.Fatalf("staged image path %q was not readable when read_image executed: %v", snapshot.advertisedPath, snapshot.stagedReadErr)
	}
	if !bytes.Equal(snapshot.stagedBytes, imageBytes) {
		t.Fatalf("staged image bytes differ from --image source")
	}
	if !snapshot.toolResultImage {
		t.Fatal("read_image tool result did not contain the staged image projection")
	}
	if snapshot.initialImageCount != 1 {
		t.Fatalf("initial inline image message count = %d, want 1", snapshot.initialImageCount)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(recordingDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read recording manifest: %v", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode recording manifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate recording manifest: %v", err)
	}
	providerCapture, err := gwtesting.LoadSessionCapture(filepath.Join(recordingDir, "provider.json"))
	if err != nil {
		t.Fatalf("load provider capture: %v", err)
	}
	if len(providerCapture.Records) == 0 {
		t.Fatal("provider capture is empty")
	}
	if violations := gwtesting.ValidateSessionCapture(filepath.Join(recordingDir, "provider.json"), providerCapture); len(violations) != 0 {
		t.Fatalf("validate provider capture: %v", violations)
	}
}

func writeStagedImageFixture(t *testing.T, dir string) (string, []byte) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{B: 255, A: 255})
	img.Set(0, 1, color.RGBA{B: 255, A: 255})
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatalf("encode image fixture: %v", err)
	}
	path := filepath.Join(dir, "source.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}
	return path, encoded.Bytes()
}

func writeStagedAudioFixture(t *testing.T, path string) {
	t.Helper()
	samples := make([]int16, audio.FrameSize)
	samples[0] = 1200
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	if err := os.WriteFile(path, pcm, 0o600); err != nil {
		t.Fatalf("write audio fixture: %v", err)
	}
}

type exactStagedImageInferencer struct {
	session     *exactStagedImageSession
	capturePath string
}

func (i *exactStagedImageInferencer) ConfigureProviderCapture(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("exact staged image provider capture path was not configured")
	}
	i.capturePath = path
	return nil
}

func (i *exactStagedImageInferencer) FlushCapture() error {
	if strings.TrimSpace(i.capturePath) == "" {
		return errors.New("exact staged image provider capture path was not configured")
	}
	return i.session.flushProviderCapture(i.capturePath)
}

func (i *exactStagedImageInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if err := i.session.capture.server(`{"type":"session.created","session":{"id":"exact-staged-image","model":"gpt-realtime-2.1-mini"}}`); err != nil {
		return nil, err
	}
	if err := i.session.capture.server(`{"type":"session.updated","session":{"id":"exact-staged-image"}}`); err != nil {
		return nil, err
	}
	if !i.session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("exact-staged-image", "gpt-realtime-2.1-mini"),
	}) {
		return nil, ctx.Err()
	}
	if !i.session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("exact-staged-image"),
	}) {
		return nil, ctx.Err()
	}
	return i.session, nil
}

type exactStagedImageSession struct {
	configDir    string
	wantImage    []byte
	capture      *exactStagedImageCapture
	recv         *messages.TypedBuffer[messages.StreamMessage]
	done         chan struct{}
	closeOnce    sync.Once
	toolCall     sync.Once
	continuation sync.Once

	mu                sync.Mutex
	advertisedPath    string
	toolCallPath      string
	stagedReadable    bool
	stagedReadErr     error
	stagedBytes       []byte
	toolResultImage   bool
	initialImageCount int
	failure           error
}

func newExactStagedImageSession(configDir string, wantImage []byte) *exactStagedImageSession {
	return &exactStagedImageSession{
		configDir: configDir,
		wantImage: append([]byte(nil), wantImage...),
		capture:   newExactStagedImageCapture(),
		recv:      messages.NewTypedBuffer[messages.StreamMessage](64),
		done:      make(chan struct{}),
	}
}

func (s *exactStagedImageSession) Send(ctx context.Context, event messages.StreamMessage) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if event.Type == messages.StreamTypeSessionUpdate {
		s.captureAdvertisedPath(event)
	}
	if event.Type == messages.StreamTypeMessageEnd {
		s.toolCall.Do(func() { s.emitToolCall() })
	}
	return true
}

func (s *exactStagedImageSession) SendMessage(ctx context.Context, message messages.Message) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if message.Role == messages.RoleTool {
		s.captureToolResult(message)
		s.continuation.Do(func() { s.emitContinuation() })
	}
	return true
}

func (s *exactStagedImageSession) SendMessageWithoutResponse(ctx context.Context, message messages.Message) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	for _, part := range message.ContentParts {
		if _, ok := part.(messages.ImagePart); ok {
			s.mu.Lock()
			s.initialImageCount++
			s.mu.Unlock()
		}
	}
	if err := s.capture.clientInitialMessage(message); err != nil {
		s.recordFailure(err)
	}
	return true
}

func (*exactStagedImageSession) SupportsCompleteMessages() bool { return true }

func (*exactStagedImageSession) SupportsCompleteMessagesWithoutResponse() bool { return true }

func (s *exactStagedImageSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *exactStagedImageSession) Done() <-chan struct{} { return s.done }

func (s *exactStagedImageSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *exactStagedImageSession) captureAdvertisedPath(event messages.StreamMessage) {
	value, ok := event.Value.(*messages.SessionUpdateValue)
	if !ok || value == nil {
		return
	}
	const marker = "Session-staged image path(s) (use one of these exact absolute paths):\n- "
	for _, definition := range value.Tools {
		if definition.Name != runtimeTools.ReadImageToolID {
			continue
		}
		for _, parameter := range definition.Parameters {
			if parameter.Name != "path" {
				continue
			}
			markerIndex := strings.Index(parameter.Description, marker)
			if markerIndex < 0 {
				s.recordFailure(errors.New("read_image path parameter omitted the staging marker"))
				return
			}
			path := parameter.Description[markerIndex+len(marker):]
			if lineEnd := strings.IndexByte(path, '\n'); lineEnd >= 0 {
				path = path[:lineEnd]
			}
			s.mu.Lock()
			s.advertisedPath = path
			s.mu.Unlock()
			if err := s.capture.clientSessionUpdate(value); err != nil {
				s.recordFailure(err)
			}
			return
		}
	}
	s.recordFailure(errors.New("session.update did not advertise read_image"))
}

func (s *exactStagedImageSession) emitToolCall() {
	s.mu.Lock()
	path := s.advertisedPath
	s.toolCallPath = path
	s.mu.Unlock()
	arguments, _ := json.Marshal(map[string]string{"path": path})
	callID := "exact-staged-image-call"
	if err := s.capture.server(fmt.Sprintf(`{"type":"response.output_item.added","item":{"type":"function_call","call_id":%q,"name":%q}}`, callID, runtimeTools.ReadImageToolID)); err != nil {
		s.recordFailure(err)
	}
	if err := s.capture.server(fmt.Sprintf(`{"type":"response.function_call_arguments.done","call_id":%q,"name":%q,"arguments":%q}`, callID, runtimeTools.ReadImageToolID, string(arguments))); err != nil {
		s.recordFailure(err)
	}
	for _, event := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue(callID, runtimeTools.ReadImageToolID)},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue(callID, runtimeTools.ReadImageToolID, string(arguments))},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		if !s.recv.Write(context.Background(), event) {
			return
		}
	}
	if err := s.capture.server(`{"type":"response.done","response":{"id":"exact-staged-image-response","status":"completed"}}`); err != nil {
		s.recordFailure(err)
	}
}

func (s *exactStagedImageSession) captureToolResult(message messages.Message) {
	s.mu.Lock()
	path := s.advertisedPath
	s.mu.Unlock()
	data, err := os.ReadFile(path)
	readable := err == nil && bytes.Equal(data, s.wantImage)
	s.mu.Lock()
	s.stagedReadable = readable
	s.stagedReadErr = err
	s.stagedBytes = append([]byte(nil), data...)
	for _, part := range message.ContentParts {
		imagePart, ok := part.(messages.ImagePart)
		if ok && bytes.Equal(imagePart.Bytes, s.wantImage) {
			s.toolResultImage = true
		}
	}
	if err != nil {
		s.failure = errors.Join(s.failure, err)
	}
	s.mu.Unlock()
	if err := s.capture.clientToolResult(message); err != nil {
		s.recordFailure(err)
	}
}

func (s *exactStagedImageSession) emitContinuation() {
	if err := s.capture.server(`{"type":"response.output_text.delta","delta":"staged image verified"}`); err != nil {
		s.recordFailure(err)
	}
	if err := s.capture.server(`{"type":"response.output_text.done","text":"staged image verified"}`); err != nil {
		s.recordFailure(err)
	}
	for _, event := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("staged image verified")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("exact-staged-image", "done")},
	} {
		if !s.recv.Write(context.Background(), event) {
			return
		}
	}
	if err := s.capture.server(`{"type":"response.done","response":{"id":"exact-staged-image-continuation","status":"completed"}}`); err != nil {
		s.recordFailure(err)
	}
	if err := s.capture.server(`{"type":"session.closed"}`); err != nil {
		s.recordFailure(err)
	}
}

func (s *exactStagedImageSession) flushProviderCapture(path string) error {
	return s.capture.flush(path)
}

func (s *exactStagedImageSession) recordFailure(err error) {
	s.mu.Lock()
	s.failure = errors.Join(s.failure, err)
	s.mu.Unlock()
}

type exactStagedImageSnapshot struct {
	advertisedPath    string
	toolCallPath      string
	stagedReadable    bool
	stagedReadErr     error
	stagedBytes       []byte
	toolResultImage   bool
	initialImageCount int
	failure           error
}

func (s *exactStagedImageSession) snapshot() exactStagedImageSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return exactStagedImageSnapshot{
		advertisedPath:    s.advertisedPath,
		toolCallPath:      s.toolCallPath,
		stagedReadable:    s.stagedReadable,
		stagedReadErr:     s.stagedReadErr,
		stagedBytes:       append([]byte(nil), s.stagedBytes...),
		toolResultImage:   s.toolResultImage,
		initialImageCount: s.initialImageCount,
		failure:           s.failure,
	}
}

var _ messages.SessionInferencer = (*exactStagedImageInferencer)(nil)
var _ messages.Session = (*exactStagedImageSession)(nil)
var _ interface {
	ConfigureProviderCapture(string) error
	FlushCapture() error
} = (*exactStagedImageInferencer)(nil)
var _ interface {
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
} = (*exactStagedImageSession)(nil)

type exactStagedImageCapture struct {
	recorder *gwtesting.RecordingWebSocketDialer
	conn     transport.Conn
	inner    *exactStagedImageCaptureConn
	initErr  error
}

func newExactStagedImageCapture() *exactStagedImageCapture {
	inner := &exactStagedImageCaptureConn{
		inbound: make(chan []byte, 32),
		closed:  make(chan struct{}),
	}
	recorder := gwtesting.NewRecordingWebSocketDialer(&exactStagedImageCaptureDialer{conn: inner}, "openai", "gpt-realtime-2.1-mini")
	conn, err := recorder.Dial("ws://exact-staged-image.invalid", nil)
	return &exactStagedImageCapture{recorder: recorder, conn: conn, inner: inner, initErr: err}
}

func (c *exactStagedImageCapture) client(payload []byte) error {
	if c == nil {
		return errors.New("exact staged image provider capture is unavailable")
	}
	if c.initErr != nil {
		return c.initErr
	}
	return c.conn.WriteMessage(1, payload)
}

func (c *exactStagedImageCapture) clientJSON(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.client(payload)
}

func (c *exactStagedImageCapture) server(payload string) error {
	if c == nil {
		return errors.New("exact staged image provider capture is unavailable")
	}
	if c.initErr != nil {
		return c.initErr
	}
	c.inner.inbound <- []byte(payload)
	_, _, err := c.conn.ReadMessage()
	return err
}

func (c *exactStagedImageCapture) clientSessionUpdate(value *messages.SessionUpdateValue) error {
	tools := make([]map[string]any, 0, len(value.Tools))
	for _, definition := range value.Tools {
		parameters := make([]map[string]any, 0, len(definition.Parameters))
		required := make([]string, 0, len(definition.Parameters))
		for _, parameter := range definition.Parameters {
			parameters = append(parameters, map[string]any{
				"name":        parameter.Name,
				"type":        parameter.Type,
				"description": parameter.Description,
			})
			if parameter.Required {
				required = append(required, parameter.Name)
			}
		}
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        definition.Name,
			"description": definition.Description,
			"parameters":  parameters,
			"required":    required,
		})
	}
	return c.clientJSON(map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"model": value.Model,
			"tools": tools,
		},
	})
}

func (c *exactStagedImageCapture) clientInitialMessage(message messages.Message) error {
	content := make([]map[string]any, 0, len(message.ContentParts))
	for _, part := range message.ContentParts {
		imagePart, ok := part.(messages.ImagePart)
		if !ok {
			continue
		}
		content = append(content, map[string]any{
			"type":      "input_image",
			"image_url": "data:" + imagePart.MediaType + ";base64," + base64.StdEncoding.EncodeToString(imagePart.Bytes),
		})
	}
	return c.clientJSON(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{"type": "message", "role": string(message.Role), "content": content},
	})
}

func (c *exactStagedImageCapture) clientToolResult(message messages.Message) error {
	if err := c.clientJSON(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type":    "function_call_output",
			"call_id": message.ToolCallID,
			"output":  message.TextContent(),
		},
	}); err != nil {
		return err
	}
	for _, part := range message.ContentParts {
		imagePart, ok := part.(messages.ImagePart)
		if !ok {
			continue
		}
		return c.clientJSON(map[string]any{
			"type": "conversation.item.create",
			"item": map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{{
					"type":      "input_image",
					"image_url": "data:" + imagePart.MediaType + ";base64," + base64.StdEncoding.EncodeToString(imagePart.Bytes),
				}},
			},
		})
	}
	return nil
}

func (c *exactStagedImageCapture) flush(path string) error {
	if c == nil {
		return errors.New("exact staged image provider capture is unavailable")
	}
	if c.initErr != nil {
		return c.initErr
	}
	capture := c.recorder.Capture()
	capture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceProviderRecorded
	sealed, err := gwtesting.SealSessionCapture(capture)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

type exactStagedImageCaptureDialer struct{ conn *exactStagedImageCaptureConn }

func (d *exactStagedImageCaptureDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

type exactStagedImageCaptureConn struct {
	inbound chan []byte
	closed  chan struct{}
	once    sync.Once
}

func (c *exactStagedImageCaptureConn) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-c.inbound:
		return 1, payload, nil
	case <-c.closed:
		return 0, nil, errors.New("exact staged image capture connection closed")
	}
}

func (c *exactStagedImageCaptureConn) WriteMessage(int, []byte) error {
	select {
	case <-c.closed:
		return errors.New("exact staged image capture connection closed")
	default:
		return nil
	}
}

func (c *exactStagedImageCaptureConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
