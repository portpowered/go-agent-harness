package acceptance_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

type heldStreamInferencer struct{ release <-chan struct{} }

func (heldStreamInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, errors.New("streaming command requested nonstreaming inference")
}

func (model heldStreamInferencer) InferStream(ctx context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	stream := make(chan messages.StreamMessage, 4)
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("visible before completion")}
	go func() {
		defer close(stream)
		select {
		case <-model.release:
			stream <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()}
			stream <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
		case <-ctx.Done():
		}
	}()
	return stream, nil
}

type firstDeltaWriter struct {
	once sync.Once
	seen chan struct{}
}

func (writer *firstDeltaWriter) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "visible before completion") {
		writer.once.Do(func() { close(writer.seen) })
	}
	return len(data), nil
}

func TestAskStreamRendersBeforeProviderFinishes(t *testing.T) {
	release := make(chan struct{})
	service := sessionwire.NewService(sessionwire.Dependencies{Inferencer: heldStreamInferencer{release: release}, RelaxValidation: true})
	command := cli.NewAskCommand(service, flags.NewAskFlags(), flags.NewLoopFlags(), flags.NewGlobalFlags()).Generate()
	output := &firstDeltaWriter{seen: make(chan struct{})}
	command.SetOut(output)
	command.SetErr(io.Discard)
	command.SetIn(strings.NewReader(""))
	command.SetArgs([]string{"--stream", "hello"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- command.ExecuteContext(ctx) }()
	select {
	case <-output.seen:
	case err := <-finished:
		t.Fatalf("command finished before displaying its first delta: %v", err)
	case <-ctx.Done():
		t.Error("CLI buffered the first delta until provider completion")
	}
	close(release)
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Error("streaming command failed to join after cancellation")
	}
}
