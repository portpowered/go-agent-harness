package strict

import (
	"context"
	"errors"
	testingpkg "testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	publicreplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestPreparedTracksExactOperations(t *testingpkg.T) {
	conn := &preparedTestConn{readType: 1, readPayload: []byte("server")}
	prepared := newPrepared(
		gwtesting.SessionCapture{Records: []gwtesting.CapturedSessionEvent{{}, {}}},
		&preparedTestDialer{conn: conn},
		&preparedTestToolExecutor{expected: 1, response: messages.ToolCallResponse{ToolCallID: "call-1", Content: "ok"}},
		nil,
		nil,
		publicreplay.StrictEvidenceScope{Protocol: true, Tools: true},
	)

	gotConn, err := prepared.Dialer().Dial("offline", map[string]string{"Authorization": "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gotConn.WriteMessage(1, []byte("client")); err != nil {
		t.Fatal(err)
	}
	if messageType, payload, err := gotConn.ReadMessage(); err != nil || messageType != 1 || string(payload) != "server" {
		t.Fatalf("ReadMessage() = (%d, %q, %v)", messageType, payload, err)
	}
	response, err := prepared.ToolExecutor().Execute(context.Background(), messages.ToolCall{ID: "call-1"})
	if err != nil || response.Content != "ok" {
		t.Fatalf("Execute() = (%+v, %v)", response, err)
	}
	if err := prepared.ValidateComplete(); err != nil {
		t.Fatalf("ValidateComplete() = %v after exact consumption", err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("Close() = %v after exact consumption", err)
	}
	if _, err := prepared.Dialer().Dial("offline", nil); !errors.Is(err, publicreplay.ErrBundleMismatch) {
		t.Fatalf("second Dial() error = %v, want ErrBundleMismatch", err)
	}
}

func TestStrictCompletionTracksIncompleteAndFirstFailure(t *testingpkg.T) {
	incomplete := &strictCompletion{expectedWire: 1, expectedTools: 1}
	if err := incomplete.validate(); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
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
	if err := incomplete.beginDial(); !errors.Is(err, publicreplay.ErrBundleMismatch) {
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
	if err := wireFailure.validate(); !errors.Is(err, publicreplay.ErrBundleIncomplete) || !errors.Is(err, wireErr) || errors.Is(err, second) {
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

func TestPreparedWrappersRejectUnavailableAndFailedOperations(t *testingpkg.T) {
	dialErr := errors.New("dial failed")
	nilDialer := newPrepared(gwtesting.SessionCapture{}, nil, &preparedTestToolExecutor{}, nil, nil, publicreplay.StrictEvidenceScope{})
	if _, err := nilDialer.Dialer().Dial("offline", nil); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("nil dialer = %v, want ErrBundleIncomplete", err)
	}
	dialFailure := newPrepared(gwtesting.SessionCapture{Records: []gwtesting.CapturedSessionEvent{{}}}, &preparedTestDialer{err: dialErr}, &preparedTestToolExecutor{}, nil, nil, publicreplay.StrictEvidenceScope{})
	if _, err := dialFailure.Dialer().Dial("offline", nil); !errors.Is(err, dialErr) {
		t.Fatalf("dial failure = %v, want underlying error", err)
	}

	readErr := errors.New("read failed")
	readFailure := newPrepared(gwtesting.SessionCapture{Records: []gwtesting.CapturedSessionEvent{{}}}, &preparedTestDialer{conn: &preparedTestConn{readErr: readErr}}, &preparedTestToolExecutor{}, nil, nil, publicreplay.StrictEvidenceScope{})
	readConn, err := readFailure.Dialer().Dial("offline", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := readConn.ReadMessage(); !errors.Is(err, readErr) {
		t.Fatalf("read failure = %v, want underlying error", err)
	}
	if err := readFailure.ValidateComplete(); !errors.Is(err, publicreplay.ErrBundleIncomplete) || !errors.Is(err, readErr) {
		t.Fatalf("read completion = %v, want incomplete/read error", err)
	}

	writeErr := errors.New("write failed")
	writeFailure := newPrepared(gwtesting.SessionCapture{Records: []gwtesting.CapturedSessionEvent{{}}}, &preparedTestDialer{conn: &preparedTestConn{writeErr: writeErr}}, &preparedTestToolExecutor{}, nil, nil, publicreplay.StrictEvidenceScope{})
	writeConn, err := writeFailure.Dialer().Dial("offline", nil)
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
	closeFailure := newPrepared(gwtesting.SessionCapture{}, &preparedTestDialer{conn: &preparedTestConn{closeErr: closeErr}}, &preparedTestToolExecutor{}, nil, nil, publicreplay.StrictEvidenceScope{})
	closeConn, err := closeFailure.Dialer().Dial("offline", nil)
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
	toolFailure := newPrepared(gwtesting.SessionCapture{}, &preparedTestDialer{}, &preparedTestToolExecutor{expected: 1, err: toolErr}, nil, nil, publicreplay.StrictEvidenceScope{})
	if _, err := toolFailure.ToolExecutor().Execute(context.Background(), messages.ToolCall{ID: "call-1"}); !errors.Is(err, toolErr) {
		t.Fatalf("tool failure = %v, want underlying error", err)
	}
	if err := toolFailure.ValidateComplete(); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("tool completion = %v, want incomplete", err)
	}
}

func TestPreparedWrappersHandleNilReceivers(t *testingpkg.T) {
	var dialer *completionDialer
	if _, err := dialer.Dial("offline", nil); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("nil dialer receiver = %v, want ErrBundleIncomplete", err)
	}
	var conn *completionConn
	if _, _, err := conn.ReadMessage(); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("nil conn ReadMessage() = %v, want ErrBundleIncomplete", err)
	}
	if err := conn.WriteMessage(1, nil); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("nil conn WriteMessage() = %v, want ErrBundleIncomplete", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("nil conn Close() = %v, want nil", err)
	}
	var executor *completionToolExecutor
	if _, err := executor.Execute(context.Background(), messages.ToolCall{}); !errors.Is(err, publicreplay.ErrToolFailure) {
		t.Fatalf("nil executor = %v, want ErrToolFailure", err)
	}
	var completion *strictCompletion
	if err := completion.validate(); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("nil completion = %v, want ErrBundleIncomplete", err)
	}
}

type preparedTestDialer struct {
	conn transport.Conn
	err  error
}

func (d *preparedTestDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, d.err
}

type preparedTestConn struct {
	readType    int
	readPayload []byte
	readErr     error
	writeErr    error
	closeErr    error
}

func (c *preparedTestConn) ReadMessage() (int, []byte, error) {
	return c.readType, c.readPayload, c.readErr
}

func (c *preparedTestConn) WriteMessage(int, []byte) error { return c.writeErr }

func (c *preparedTestConn) Close() error { return c.closeErr }

type preparedTestToolExecutor struct {
	expected int
	response messages.ToolCallResponse
	err      error
}

func (e *preparedTestToolExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	return e.response, e.err
}

func (e *preparedTestToolExecutor) ExpectedToolCalls() int { return e.expected }

var _ transport.Dialer = (*preparedTestDialer)(nil)
var _ transport.Conn = (*preparedTestConn)(nil)
var _ messages.ToolExecutor = (*preparedTestToolExecutor)(nil)
