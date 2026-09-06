package acceptance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/spf13/cobra"
)

type outputInferencer struct{ events []messages.StreamMessage }

func (model outputInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{Message: messages.ReconstructModelMessageFromDeltas(model.events)}, nil
}

func (model outputInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	stream := make(chan messages.StreamMessage, len(model.events))
	for _, event := range model.events {
		stream <- event
	}
	close(stream)
	return stream, nil
}

func outputCommand(events ...messages.StreamMessage) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	service := sessionwire.NewService(sessionwire.Dependencies{Inferencer: outputInferencer{events: events}, RelaxValidation: true})
	command := cli.NewAskCommand(service, flags.NewAskFlags(), flags.NewLoopFlags(), flags.NewGlobalFlags()).Generate()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetIn(strings.NewReader(""))
	return command, stdout, stderr
}

func outputEvent(kind messages.StreamMessageType, value messages.StreamMessageValue) messages.StreamMessage {
	return messages.StreamMessage{Type: kind, Role: messages.RoleAssistant, Value: value}
}

func TestAskNonStreamingJSONRetainsMessageContent(t *testing.T) {
	command, stdout, _ := outputCommand(
		outputEvent(messages.StreamTypeMessageStart, messages.NewMessageStartValue()),
		outputEvent(messages.StreamTypeTextDelta, messages.NewTextDeltaValue("answer")),
		outputEvent(messages.StreamTypeMessageEnd, messages.NewMessageEndValue(messages.TokenUsage{})),
	)
	command.SetArgs([]string{"--output-json", "hello"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid JSON output %q: %v", stdout.String(), err)
	}
	if len(envelope.Messages) != 2 || !bytes.Contains(envelope.Messages[0], []byte(`"hello"`)) || !bytes.Contains(envelope.Messages[1], []byte(`"answer"`)) {
		t.Fatalf("typed message content lost: %s", stdout.String())
	}
}

func TestAskBinaryContentHasNoPresentationBytes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "final", true: "stream"}[stream], func(t *testing.T) {
			pcm := []byte{1, 2, 3, 4, 5, 6}
			command, stdout, stderr := outputCommand(
				outputEvent(messages.StreamTypeMessageStart, messages.NewMessageStartValue()),
				outputEvent(messages.StreamTypeAudioStart, messages.NewAudioStartValue()),
				outputEvent(messages.StreamTypeAudioDelta, messages.NewAudioDeltaValue(pcm)),
				outputEvent(messages.StreamTypeAudioEnd, messages.NewAudioEndValue()),
				outputEvent(messages.StreamTypeMessageEnd, messages.NewMessageEndValue(messages.TokenUsage{})),
			)
			args := []string{"--output-modality", "audio", "--output-json", "hello"}
			if stream {
				args = append([]string{"--stream"}, args...)
			}
			command.SetArgs(args)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(stdout.Bytes(), pcm) || stderr.Len() != 0 {
				t.Fatalf("binary output %v stderr %q, want exact PCM %v", stdout.Bytes(), stderr.String(), pcm)
			}
		})
	}
}

func TestAskRefusalUsesHostStderr(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		marker string
		stream bool
	}{
		{name: "plain", args: []string{"hello"}, marker: "[REFUSAL] fixture refusal"},
		{name: "JSON", args: []string{"--output-json", "hello"}, marker: `"type":"refusal"`},
		{name: "stream JSON", args: []string{"--stream", "--output-json", "hello"}, marker: `"type":"refusal"`, stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, stdout, stderr := outputCommand(
				outputEvent(messages.StreamTypeMessageStart, messages.NewMessageStartValue()),
				outputEvent(messages.StreamTypeRefusal, messages.NewRefusalValue("fixture refusal")),
				outputEvent(messages.StreamTypeMessageEnd, messages.NewMessageEndValue(messages.TokenUsage{})),
			)
			command.SetArgs(tc.args)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stderr.String(), tc.marker) || !strings.Contains(stderr.String(), "fixture refusal") {
				t.Fatalf("missing host refusal: %q", stderr.String())
			}
			if strings.Contains(stdout.String(), `"type":"refusal"`) {
				t.Fatalf("refusal diagnostic mixed into output: %s", stdout.String())
			}
			if tc.stream && !strings.Contains(stdout.String(), `"type":"MESSAGE.START"`) {
				t.Fatalf("stream event envelope lost: %s", stdout.String())
			}
		})
	}
}

func TestAskStreamReasoningPrecedesText(t *testing.T) {
	command, stdout, _ := outputCommand(
		outputEvent(messages.StreamTypeMessageStart, messages.NewMessageStartValue()),
		outputEvent(messages.StreamTypeReasoningStart, messages.NewReasoningStartValue()),
		outputEvent(messages.StreamTypeReasoningDelta, messages.NewReasoningDeltaValue("thinking")),
		outputEvent(messages.StreamTypeReasoningEnd, messages.NewReasoningEndValue()),
		outputEvent(messages.StreamTypeTextDelta, messages.NewTextDeltaValue("stream answer")),
		outputEvent(messages.StreamTypeMessageEnd, messages.NewMessageEndValue(messages.TokenUsage{})),
	)
	command.SetArgs([]string{"--stream", "--output-reasoning-tokens", "hello"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "thinkingstream answer" {
		t.Fatalf("stream presentation = %q", stdout.String())
	}
}

func TestAskEmptyBinaryContentWarnsAtHost(t *testing.T) {
	for _, stream := range []bool{false, true} {
		command, stdout, stderr := outputCommand(
			outputEvent(messages.StreamTypeMessageStart, messages.NewMessageStartValue()),
			outputEvent(messages.StreamTypeTextDelta, messages.NewTextDeltaValue("text only")),
			outputEvent(messages.StreamTypeMessageEnd, messages.NewMessageEndValue(messages.TokenUsage{})),
		)
		args := []string{"--output-modality", "audio", "hello"}
		if stream {
			args = append([]string{"--stream"}, args...)
		}
		command.SetArgs(args)
		if err := command.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "no audio content") {
			t.Fatalf("empty binary stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	}
}

type failedOutputWriter struct{}

func (failedOutputWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAskStructuredOutputPreservesWriterFailure(t *testing.T) {
	for _, args := range [][]string{
		{"--output-json", "hello"},
		{"--output-modality", "audio", "hello"},
		{"--stream", "--output-modality", "audio", "hello"},
	} {
		command, _, _ := outputCommand(
			outputEvent(messages.StreamTypeMessageStart, messages.NewMessageStartValue()),
			outputEvent(messages.StreamTypeTextDelta, messages.NewTextDeltaValue("answer")),
			outputEvent(messages.StreamTypeAudioDelta, messages.NewAudioDeltaValue([]byte{1, 2})),
			outputEvent(messages.StreamTypeMessageEnd, messages.NewMessageEndValue(messages.TokenUsage{})),
		)
		command.SetOut(failedOutputWriter{})
		command.SetArgs(args)
		if err := command.ExecuteContext(t.Context()); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("writer failure = %v, want original error", err)
		}
	}
}

func TestAskRefusalPreservesDiagnosticWriterFailure(t *testing.T) {
	for _, args := range [][]string{{"hello"}, {"--output-json", "hello"}, {"--stream", "hello"}, {"--stream", "--output-json", "hello"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			command, _, _ := outputCommand(
				outputEvent(messages.StreamTypeMessageStart, messages.NewMessageStartValue()),
				outputEvent(messages.StreamTypeRefusal, messages.NewRefusalValue("fixture refusal")),
				outputEvent(messages.StreamTypeMessageEnd, messages.NewMessageEndValue(messages.TokenUsage{})),
			)
			command.SetArgs(args)
			command.SetErr(failedOutputWriter{})
			if err := command.ExecuteContext(t.Context()); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("diagnostic failure = %v", err)
			}
		})
	}
}
