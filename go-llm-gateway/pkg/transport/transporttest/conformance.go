// Package transporttest provides a reusable behavioral conformance suite for
// implementations of package transport.
package transporttest

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// Message is an expected message used by the S11 conformance suite.
type Message struct {
	Type    int
	Payload []byte
}

// DialCall records one observed Dial invocation.
type DialCall struct {
	Endpoint string
	Headers  map[string]string
}

// Observer exposes implementation-specific observations needed by S11.
// Implementations should return snapshots, not mutable internal state.
type Observer interface {
	DialCalls() []DialCall
	WrittenMessages() []Message
	CloseCount() int
}

// FailureCase supplies a fresh dialer and the error expected from one
// operation. MatchErr may additionally assert a typed error with errors.As.
type FailureCase struct {
	New      func() transport.Dialer
	WantErr  error
	MatchErr func(error) bool
}

// ConformanceHarness supplies valid and failure fixtures for RunS11.
// Factories are called independently for each subtest and must not share
// connection state.
type ConformanceHarness struct {
	Endpoint     string
	Headers      map[string]string
	Inbound      []Message
	Outbound     []Message
	NewValid     func() (transport.Dialer, Observer)
	DialFailure  FailureCase
	ReadFailure  FailureCase
	WriteFailure FailureCase
}

// RunS11 runs the shared runtime conformance suite for a message transport.
// It verifies dial forwarding and ownership, ordered typed messages, exact
// writes, close observation, and distinct typed dial/read/write failures.
func RunS11(t *testing.T, h ConformanceHarness) {
	t.Helper()
	if err := validateHarness(h); err != nil {
		t.Fatal(err)
	}
	t.Run("valid lifecycle", func(t *testing.T) { runValid(t, h) })
	t.Run("dial failure", func(t *testing.T) { runFailure(t, h, h.DialFailure, "dial") })
	t.Run("read failure", func(t *testing.T) { runFailure(t, h, h.ReadFailure, "read") })
	t.Run("write failure", func(t *testing.T) { runFailure(t, h, h.WriteFailure, "write") })
}

// RunConformance is an intentionally descriptive alias for RunS11.
func RunConformance(t *testing.T, h ConformanceHarness) { RunS11(t, h) }

func validateHarness(h ConformanceHarness) error {
	if h.NewValid == nil {
		return errors.New("transport S11: NewValid is required")
	}
	if h.Endpoint == "" {
		return errors.New("transport S11: Endpoint is required")
	}
	if len(h.Headers) == 0 {
		return errors.New("transport S11: at least one header is required")
	}
	if err := validateMessages("Inbound", h.Inbound); err != nil {
		return err
	}
	if err := validateMessages("Outbound", h.Outbound); err != nil {
		return err
	}
	for name, failure := range map[string]FailureCase{
		"DialFailure": h.DialFailure, "ReadFailure": h.ReadFailure, "WriteFailure": h.WriteFailure,
	} {
		if failure.New == nil {
			return fmt.Errorf("transport S11: %s.New is required", name)
		}
		if failure.WantErr == nil {
			return fmt.Errorf("transport S11: %s.WantErr is required", name)
		}
	}
	errs := []error{h.DialFailure.WantErr, h.ReadFailure.WantErr, h.WriteFailure.WantErr}
	for i := range errs {
		for j := i + 1; j < len(errs); j++ {
			if errors.Is(errs[i], errs[j]) || errors.Is(errs[j], errs[i]) {
				return errors.New("transport S11: dial, read, and write errors must be distinct")
			}
		}
	}
	return nil
}

func validateMessages(name string, messages []Message) error {
	if len(messages) < 2 {
		return fmt.Errorf("transport S11: %s must contain at least two messages", name)
	}
	for i, message := range messages {
		if len(message.Payload) == 0 {
			return fmt.Errorf("transport S11: %s[%d] payload must be non-empty", name, i)
		}
	}
	return nil
}

func runValid(t *testing.T, h ConformanceHarness) {
	t.Helper()
	dialer, observer := h.NewValid()
	if dialer == nil || observer == nil {
		t.Fatal("transport S11: NewValid must return a dialer and observer")
	}
	headers := cloneHeaders(h.Headers)
	conn, err := dialer.Dial(h.Endpoint, headers)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if conn == nil {
		t.Fatal("Dial returned a nil connection with nil error")
	}
	if !sameHeaders(headers, h.Headers) {
		t.Fatal("Dial mutated caller-owned headers")
	}
	for key := range headers {
		headers[key] += "-caller-reused"
		break
	}
	calls := observer.DialCalls()
	if len(calls) != 1 {
		t.Fatalf("observed %d Dial calls, want exactly one", len(calls))
	}
	if calls[0].Endpoint != h.Endpoint || !sameHeaders(calls[0].Headers, h.Headers) {
		t.Fatalf("observed Dial = %#v, want endpoint %q and complete headers %#v", calls[0], h.Endpoint, h.Headers)
	}
	for i, want := range h.Inbound {
		gotType, gotPayload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage[%d]: %v", i, err)
		}
		if gotType != want.Type || !bytes.Equal(gotPayload, want.Payload) {
			t.Fatalf("ReadMessage[%d] = (%d, %v), want (%d, %v)", i, gotType, gotPayload, want.Type, want.Payload)
		}
	}
	for i, want := range h.Outbound {
		payload := append([]byte(nil), want.Payload...)
		if err := conn.WriteMessage(want.Type, payload); err != nil {
			t.Fatalf("WriteMessage[%d]: %v", i, err)
		}
		if !bytes.Equal(payload, want.Payload) {
			t.Fatalf("WriteMessage[%d] mutated caller-owned payload", i)
		}
		payload[0] ^= 0xff
	}
	writes := observer.WrittenMessages()
	if len(writes) != len(h.Outbound) {
		t.Fatalf("observed %d writes, want %d", len(writes), len(h.Outbound))
	}
	for i, want := range h.Outbound {
		if writes[i].Type != want.Type || !bytes.Equal(writes[i].Payload, want.Payload) {
			t.Fatalf("observed write[%d] = %#v, want %#v", i, writes[i], want)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := observer.CloseCount(); got != 1 {
		t.Fatalf("observed %d closes, want exactly one", got)
	}
}

func runFailure(t *testing.T, h ConformanceHarness, failure FailureCase, operation string) {
	t.Helper()
	dialer := failure.New()
	if dialer == nil {
		t.Fatal("failure factory returned a nil dialer")
	}
	conn, err := dialer.Dial(h.Endpoint, cloneHeaders(h.Headers))
	if operation == "dial" {
		if conn != nil {
			t.Fatal("failed Dial returned a non-nil connection")
		}
		checkFailure(t, err, failure)
		return
	}
	if err != nil || conn == nil {
		t.Fatalf("failure fixture Dial = (%v, %v), want a usable connection", conn, err)
	}
	defer func() { _ = conn.Close() }()
	if operation == "read" {
		_, _, err = conn.ReadMessage()
	} else {
		message := h.Outbound[0]
		err = conn.WriteMessage(message.Type, append([]byte(nil), message.Payload...))
	}
	checkFailure(t, err, failure)
}

func checkFailure(t *testing.T, got error, want FailureCase) {
	t.Helper()
	if got == nil {
		t.Fatal("operation returned nil error")
	}
	if !errors.Is(got, want.WantErr) {
		t.Fatalf("error %v does not preserve %v", got, want.WantErr)
	}
	if want.MatchErr != nil && !want.MatchErr(got) {
		t.Fatalf("error %v failed the typed error matcher", got)
	}
}

func cloneHeaders(headers map[string]string) map[string]string {
	clone := make(map[string]string, len(headers))
	for key, value := range headers {
		clone[key] = value
	}
	return clone
}

func sameHeaders(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range b {
		got, ok := a[key]
		if !ok || got != value {
			return false
		}
	}
	return true
}
