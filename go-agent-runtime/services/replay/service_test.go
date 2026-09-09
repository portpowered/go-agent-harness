package replay

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestStrictPreparedValidateCompleteRequiresPreparation(t *testing.T) {
	if err := (StrictPrepared{}).ValidateComplete(); !errors.Is(err, ErrBundleIncomplete) {
		t.Fatalf("ValidateComplete() error = %v, want ErrBundleIncomplete", err)
	}
}

func TestReplayPublicValueContracts(t *testing.T) {
	if got := ErrBundleIncomplete.Error(); got != "replay bundle is incomplete" {
		t.Fatalf("error code = %q, want stable text", got)
	}
	if !(CaptureInspection{Kind: CaptureKindRealtime}).IsRealtime() {
		t.Fatal("realtime capture was not classified as realtime")
	}
	if (CaptureInspection{Kind: CaptureKindTurn}).IsRealtime() {
		t.Fatal("turn capture was classified as realtime")
	}
}

func TestStrictPreparedBuilderTracksExactOperations(t *testing.T) {
	conn := &replayTestConn{readType: 1, readPayload: []byte("server")}
	prepared := StrictPreparedBuilder{}.Build(
		gwtesting.SessionCapture{},
		&replayTestDialer{conn: conn},
		&replayTestToolExecutor{response: messages.ToolCallResponse{ToolCallID: "call-1", Content: "ok"}},
		nil,
		nil,
		StrictEvidenceScope{Protocol: true, Tools: true},
		2,
		1,
	)

	gotConn, err := prepared.Dialer.Dial("offline", map[string]string{"Authorization": "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gotConn.WriteMessage(1, []byte("client")); err != nil {
		t.Fatal(err)
	}
	if messageType, payload, err := gotConn.ReadMessage(); err != nil || messageType != 1 || string(payload) != "server" {
		t.Fatalf("ReadMessage() = (%d, %q, %v)", messageType, payload, err)
	}
	response, err := prepared.ToolExecutor.Execute(context.Background(), messages.ToolCall{ID: "call-1"})
	if err != nil || response.Content != "ok" {
		t.Fatalf("Execute() = (%+v, %v)", response, err)
	}
	if err := prepared.ValidateComplete(); err != nil {
		t.Fatalf("ValidateComplete() = %v after exact consumption", err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("Close() = %v after exact consumption", err)
	}
	if _, err := prepared.Dialer.Dial("offline", nil); !errors.Is(err, ErrBundleMismatch) {
		t.Fatalf("second Dial() error = %v, want ErrBundleMismatch", err)
	}
}

func TestStrictCompletionTracksIncompleteAndFirstFailure(t *testing.T) {
	incomplete := &strictCompletion{expectedWire: 1, expectedTools: 1}
	if err := incomplete.validate(); !errors.Is(err, ErrBundleIncomplete) {
		t.Fatalf("unstarted validation = %v, want ErrBundleIncomplete", err)
	}
	if err := incomplete.beginDial(); err != nil {
		t.Fatal(err)
	}
	incomplete.markWire()
	incomplete.markTool()
	if err := incomplete.validate(); err != nil {
		t.Fatalf("exact completion = %v", err)
	}
	if err := incomplete.beginDial(); !errors.Is(err, ErrBundleMismatch) {
		t.Fatalf("second beginDial() = %v, want ErrBundleMismatch", err)
	}

	first := errors.New("first completion failure")
	second := errors.New("second completion failure")
	failed := &strictCompletion{expectedWire: 2}
	failed.fail(nil)
	failed.fail(first)
	failed.fail(second)
	if err := failed.validate(); !errors.Is(err, first) || errors.Is(err, second) {
		t.Fatalf("first failure was not retained: %v", err)
	}

	wireErr := errors.New("wire ended early")
	wireFailure := &strictCompletion{expectedWire: 2}
	wireFailure.failWire(nil)
	wireFailure.failWire(wireErr)
	wireFailure.failWire(second)
	if err := wireFailure.validate(); !errors.Is(err, ErrBundleIncomplete) || !errors.Is(err, wireErr) || errors.Is(err, second) {
		t.Fatalf("wire failure = %v", err)
	}

	fullyConsumed := &strictCompletion{expectedWire: 1}
	if err := fullyConsumed.beginDial(); err != nil {
		t.Fatal(err)
	}
	fullyConsumed.markWire()
	fullyConsumed.failWire(wireErr)
	if err := fullyConsumed.validate(); err != nil {
		t.Fatalf("fully consumed wire was failed by late error: %v", err)
	}
}

func TestStrictPreparedWrappersRejectUnavailableAndFailedOperations(t *testing.T) {
	dialErr := errors.New("dial failed")
	if _, err := (StrictPreparedBuilder{}).Build(gwtesting.SessionCapture{}, nil, &replayTestToolExecutor{}, nil, nil, StrictEvidenceScope{}, 0, 0).Dialer.Dial("offline", nil); !errors.Is(err, ErrBundleIncomplete) {
		t.Fatalf("nil dialer = %v, want ErrBundleIncomplete", err)
	}
	dialFailure := StrictPreparedBuilder{}.Build(gwtesting.SessionCapture{}, &replayTestDialer{err: dialErr}, &replayTestToolExecutor{}, nil, nil, StrictEvidenceScope{}, 0, 0)
	if _, err := dialFailure.Dialer.Dial("offline", nil); !errors.Is(err, dialErr) {
		t.Fatalf("dial failure = %v, want underlying error", err)
	}

	nilConnection := StrictPreparedBuilder{}.Build(gwtesting.SessionCapture{}, &replayTestDialer{}, &replayTestToolExecutor{}, nil, nil, StrictEvidenceScope{}, 0, 0)
	if _, err := nilConnection.Dialer.Dial("offline", nil); !errors.Is(err, ErrBundleIncomplete) {
		t.Fatalf("nil connection = %v, want ErrBundleIncomplete", err)
	}

	readErr := errors.New("read failed")
	readFailure := StrictPreparedBuilder{}.Build(gwtesting.SessionCapture{}, &replayTestDialer{conn: &replayTestConn{readErr: readErr}}, &replayTestToolExecutor{}, nil, nil, StrictEvidenceScope{}, 1, 0)
	readConn, err := readFailure.Dialer.Dial("offline", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := readConn.ReadMessage(); !errors.Is(err, readErr) {
		t.Fatalf("read failure = %v, want underlying error", err)
	}
	if err := readFailure.ValidateComplete(); !errors.Is(err, ErrBundleIncomplete) || !errors.Is(err, readErr) {
		t.Fatalf("read completion = %v, want incomplete/read error", err)
	}

	writeErr := errors.New("write failed")
	writeFailure := StrictPreparedBuilder{}.Build(gwtesting.SessionCapture{}, &replayTestDialer{conn: &replayTestConn{writeErr: writeErr}}, &replayTestToolExecutor{}, nil, nil, StrictEvidenceScope{}, 1, 0)
	writeConn, err := writeFailure.Dialer.Dial("offline", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeConn.WriteMessage(1, []byte("client")); !errors.Is(err, writeErr) {
		t.Fatalf("write failure = %v, want underlying error", err)
	}
	if err := writeFailure.ValidateComplete(); !errors.Is(err, writeErr) {
		t.Fatalf("write completion = %v, want underlying error", err)
	}

	closeErr := errors.New("close failed")
	closeFailure := StrictPreparedBuilder{}.Build(gwtesting.SessionCapture{}, &replayTestDialer{conn: &replayTestConn{closeErr: closeErr}}, &replayTestToolExecutor{}, nil, nil, StrictEvidenceScope{}, 0, 0)
	closeConn, err := closeFailure.Dialer.Dial("offline", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeConn.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("close failure = %v, want underlying error", err)
	}
	if err := closeFailure.ValidateComplete(); !errors.Is(err, closeErr) {
		t.Fatalf("close completion = %v, want underlying error", err)
	}

	toolErr := errors.New("tool failed")
	toolFailure := StrictPreparedBuilder{}.Build(gwtesting.SessionCapture{}, &replayTestDialer{}, &replayTestToolExecutor{err: toolErr}, nil, nil, StrictEvidenceScope{}, 0, 1)
	if _, err := toolFailure.ToolExecutor.Execute(context.Background(), messages.ToolCall{ID: "call-1"}); !errors.Is(err, toolErr) {
		t.Fatalf("tool failure = %v, want underlying error", err)
	}
	if err := toolFailure.ValidateComplete(); !errors.Is(err, ErrBundleIncomplete) {
		t.Fatalf("tool completion = %v, want incomplete", err)
	}

	nilTool := StrictPreparedBuilder{}.Build(gwtesting.SessionCapture{}, &replayTestDialer{}, nil, nil, nil, StrictEvidenceScope{}, 0, 1)
	if _, err := nilTool.ToolExecutor.Execute(context.Background(), messages.ToolCall{ID: "call-1"}); !errors.Is(err, ErrToolFailure) {
		t.Fatalf("nil tool executor = %v, want ErrToolFailure", err)
	}
}

func TestStrictPreparedWrappersHandleNilReceivers(t *testing.T) {
	var dialer *completionDialer
	if _, err := dialer.Dial("offline", nil); !errors.Is(err, ErrBundleIncomplete) {
		t.Fatalf("nil dialer receiver = %v, want ErrBundleIncomplete", err)
	}
	var conn *completionConn
	if _, _, err := conn.ReadMessage(); !errors.Is(err, ErrBundleIncomplete) {
		t.Fatalf("nil conn ReadMessage() = %v, want ErrBundleIncomplete", err)
	}
	if err := conn.WriteMessage(1, nil); !errors.Is(err, ErrBundleIncomplete) {
		t.Fatalf("nil conn WriteMessage() = %v, want ErrBundleIncomplete", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("nil conn Close() = %v, want nil", err)
	}
	var executor *completionToolExecutor
	if _, err := executor.Execute(context.Background(), messages.ToolCall{}); !errors.Is(err, ErrToolFailure) {
		t.Fatalf("nil executor = %v, want ErrToolFailure", err)
	}
	var completion *strictCompletion
	if err := completion.validate(); !errors.Is(err, ErrBundleIncomplete) {
		t.Fatalf("nil completion = %v, want ErrBundleIncomplete", err)
	}
}

type replayTestDialer struct {
	conn transport.Conn
	err  error
}

func (d *replayTestDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, d.err
}

type replayTestConn struct {
	readType    int
	readPayload []byte
	readErr     error
	writeErr    error
	closeErr    error
}

func (c *replayTestConn) ReadMessage() (int, []byte, error) {
	return c.readType, c.readPayload, c.readErr
}

func (c *replayTestConn) WriteMessage(int, []byte) error { return c.writeErr }

func (c *replayTestConn) Close() error { return c.closeErr }

type replayTestToolExecutor struct {
	response messages.ToolCallResponse
	err      error
}

func (e *replayTestToolExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	return e.response, e.err
}

var _ transport.Dialer = (*replayTestDialer)(nil)
var _ transport.Conn = (*replayTestConn)(nil)
var _ messages.ToolExecutor = (*replayTestToolExecutor)(nil)
