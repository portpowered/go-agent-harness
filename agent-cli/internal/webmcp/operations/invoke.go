package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	// ReceiptVersion versions the bounded dispatch receipt.
	ReceiptVersion = "webmcp.invoke-receipt.v1"
	// ReceiptMaxBytes bounds one encoded receipt line.
	ReceiptMaxBytes = 1024
)

// Receipt is the bounded stderr handoff emitted once a browser invocation
// has been dispatched. It intentionally contains no page input/output,
// schema, endpoint, or credential data.
type Receipt struct {
	Version      string `json:"version"`
	InvocationID string `json:"invocation_id"`
	ToolRef      string `json:"tool_ref"`
	State        string `json:"state"`
}

// InvokeRequest is one direct invocation. Receipts receives the dispatch
// receipt; Interrupted reports whether the user interrupted the command, in
// which case a failure before dispatch is reported as a pre-dispatch
// cancellation and a dispatched call is reconciled with a bounded cancel.
type InvokeRequest struct {
	Selector    Selector
	Input       InvocationInput
	Reason      string
	Timeout     time.Duration
	Receipts    io.Writer
	Interrupted func() bool
}

func (r InvokeRequest) interrupted() bool {
	return r.Interrupted != nil && r.Interrupted()
}

// dispatched is an accepted invocation awaiting its terminal result.
type dispatched struct {
	result    webmcp.InvokeResult
	selected  webmcp.PageKey
	receiptID webmcp.InvocationID
	toolRef   webmcp.ToolRef
}

// Invoke selects the target, resolves the tool, dispatches the call, writes
// the dispatch receipt, and waits for the terminal result.
func Invoke(ctx context.Context, broker webmcp.Broker, request InvokeRequest) (Invocation, error) {
	selected, err := EnsureSelection(ctx, broker, request.Selector)
	if err != nil {
		return Invocation{}, request.beforeDispatch("", err)
	}
	toolRef, input, err := ResolveInvocation(ctx, broker, request.Input)
	if err != nil {
		return Invocation{}, request.beforeDispatch("", err)
	}
	invokeCtx := ctx
	if request.Timeout > 0 {
		var cancel context.CancelFunc
		invokeCtx, cancel = context.WithTimeout(ctx, request.Timeout)
		defer cancel()
	}
	result, err := broker.Invoke(invokeCtx, webmcp.InvokeRequest{ToolRef: toolRef, Input: input, Reason: request.Reason})
	if err != nil {
		return Invocation{}, request.beforeDispatch(toolRef, err)
	}
	receiptID, err := WriteReceipt(request.Receipts, result, toolRef)
	if err != nil {
		return Invocation{}, err
	}
	return request.await(invokeCtx, broker, dispatched{result: result, selected: selected.Key, receiptID: receiptID, toolRef: toolRef})
}

func (r InvokeRequest) beforeDispatch(toolRef webmcp.ToolRef, err error) error {
	if r.interrupted() {
		return CanceledBeforeDispatch(toolRef)
	}
	return err
}

func (r InvokeRequest) await(ctx context.Context, broker webmcp.Broker, call dispatched) (Invocation, error) {
	if r.interrupted() {
		return Invocation{}, call.reconcile(ctx, broker)
	}
	result, err := WaitInvocation(ctx, broker, call.result)
	if err != nil {
		if r.interrupted() {
			return Invocation{}, call.reconcile(ctx, broker)
		}
		return Invocation{}, err
	}
	if result.ErrorCode != "" || result.State.Failed() {
		return Invocation{}, InvocationResultError(result, call.toolRef)
	}
	output := result.Output
	if len(bytes.TrimSpace(output)) == 0 {
		output = json.RawMessage(jsonNull)
	}
	status := string(result.State)
	if status == "" {
		status = string(webmcp.InvocationDispatched)
	}
	return Invocation{
		InvocationID: string(call.receiptID),
		ToolRef:      string(call.toolRef),
		Status:       status,
		Output:       append(json.RawMessage(nil), output...),
	}, nil
}

func (d dispatched) reconcile(ctx context.Context, broker webmcp.Broker) error {
	return ReconcileInterrupt(ctx, broker, d.result, d.selected, d.receiptID, d.toolRef)
}

// WriteReceipt writes one bounded receipt line for a dispatched invocation
// and returns the browser-facing invocation ID it names. A receipt that
// cannot be written is an invocation failure with an unknown side effect.
func WriteReceipt(out io.Writer, result webmcp.InvokeResult, toolRef webmcp.ToolRef) (webmcp.InvocationID, error) {
	invocationID := result.BrowserInvocationID
	if invocationID == "" {
		invocationID = result.InvocationID
	}
	if invocationID == "" {
		return "", receiptError(invocationID, toolRef, errors.New("browser returned no invocation ID"))
	}
	if out == nil {
		return "", receiptError(invocationID, toolRef, errors.New("stderr writer is unavailable"))
	}
	receipt, err := json.Marshal(Receipt{
		Version:      ReceiptVersion,
		InvocationID: string(invocationID),
		ToolRef:      string(toolRef),
		State:        string(webmcp.InvocationDispatched),
	})
	if err != nil {
		return "", receiptError(invocationID, toolRef, err)
	}
	if len(receipt)+1 > ReceiptMaxBytes {
		return "", receiptError(invocationID, toolRef, errors.New("receipt exceeds the bounded size"))
	}
	if err := writeFlushed(out, append(receipt, '\n')); err != nil {
		return "", receiptError(invocationID, toolRef, err)
	}
	return invocationID, nil
}

// writeFlushed writes every byte of line and flushes a buffered writer.
func writeFlushed(out io.Writer, line []byte) error {
	for len(line) > 0 {
		written, err := out.Write(line)
		if written > 0 {
			line = line[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	if flusher, ok := out.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

func receiptError(invocationID webmcp.InvocationID, toolRef webmcp.ToolRef, cause error) error {
	err := webmcp.NewClassifiedError(webmcp.ErrorInvocationFailed, "the WebMCP dispatch receipt could not be written", map[string]any{
		detailInvocationID:      string(invocationID),
		detailToolRef:           string(toolRef),
		detailPhase:             "dispatch_receipt",
		detailSideEffectUnknown: true,
	})
	err.Cause = cause
	return err
}
