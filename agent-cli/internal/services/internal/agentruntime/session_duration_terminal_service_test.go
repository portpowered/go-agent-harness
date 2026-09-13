package agentruntime

import (
	"bytes"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionDurationTerminalAdapterPublishesSynthesizedTerminal(t *testing.T) {
	var out bytes.Buffer
	var seen []messages.StreamMessage
	artifacts := terminalArtifactFunc(func(msg messages.StreamMessage) error {
		seen = append(seen, msg)
		return nil
	})
	if err := writeMaxDurationTerminal(&out, artifacts, messages.TerminalOutputPartial); err != nil {
		t.Fatalf("write max-duration terminal: %v", err)
	}
	if len(seen) != 1 || !bytes.Contains(out.Bytes(), []byte("max_duration")) {
		t.Fatalf("terminal publication = %d artifacts, output %q", len(seen), out.String())
	}
	value, ok := seen[0].Value.(*messages.SessionCloseValue)
	if !ok || value.TerminalProvenance != messages.TerminalProvenanceLoop || value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("terminal metadata = %+v", seen[0].Value)
	}
}

func TestSessionDurationTerminalAdapterProjectsOutputAndPrecedence(t *testing.T) {
	provider := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal(
		"session", "provider-close", "provider_close", messages.TerminalReasonProviderClose,
		messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
	)}
	value := provider.Value.(*messages.SessionCloseValue)
	inferencer := &sessionDurationAdmissionInferencer{session: &sessionDurationAdmissionSession{
		providerTerminal: provider, providerTerminalValue: value, providerTerminalSeen: true,
	}}
	state := newSessionDurationTerminalState(inferencer)
	state.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	state.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	if got := state.outputState(); got != messages.TerminalOutputComplete {
		t.Fatalf("output state = %q", got)
	}
	loopClose := messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("", "loop-close")}
	if _, write := state.admitTerminal(true, loopClose); write || state.terminalWritten {
		t.Fatal("loop close was admitted as provider evidence")
	}
	if _, write := state.admitTerminal(true, provider); !write || !state.terminalWritten {
		t.Fatal("provider close was not admitted")
	}
}

func TestSessionDurationTerminalAdapterPreservesErrorIdentity(t *testing.T) {
	runtimeErr := errors.New("runtime identity")
	closeErr := errors.New("close identity")
	bindingErr := errors.New("binding identity")
	err := sessionDurationLifecycleError(runtimeErr, closeErr, bindingErr)
	if !errors.Is(err, runtimeErr) || !errors.Is(err, closeErr) || !errors.Is(err, bindingErr) {
		t.Fatalf("lifecycle errors = %v", err)
	}
	transportErr := errors.New("transport identity")
	if got := sessionTransportError(transportErr); !errors.Is(got, transportErr) {
		t.Fatalf("transport error = %v", got)
	}
}

type terminalArtifactFunc func(messages.StreamMessage) error

func (f terminalArtifactFunc) Accept(msg messages.StreamMessage) error { return f(msg) }
func (terminalArtifactFunc) Flush() error                              { return nil }
func (terminalArtifactFunc) Close() error                              { return nil }
