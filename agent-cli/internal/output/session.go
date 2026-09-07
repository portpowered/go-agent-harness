package output

import (
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"io"
)

// SessionPresentation selects host rendering without changing session behavior.
type SessionPresentation struct {
	JSON     bool
	Stream   bool
	Modality string
	Model    string
}

func (p SessionPresentation) binary() bool { return p.Modality != "" && p.Modality != "text" }

// WriteResult renders typed final messages and routes refusals to diagnostics.
func (p SessionPresentation) WriteResult(out, diagnostics io.Writer, msgs []messages.Message, text string) error {
	for _, message := range msgs {
		if message.Role != messages.RoleAssistant || message.Refusal == "" {
			continue
		}
		if err := p.writeRefusal(diagnostics, message.Refusal); err != nil {
			return err
		}
	}
	if p.binary() {
		size, err := WriteBinaryModalityMessages(out, msgs, p.Modality)
		return p.checkBinary(diagnostics, size, err)
	}
	if p.JSON {
		return WriteMessagesJSON(out, msgs)
	}
	if err := WriteSessionText(out, text, p.Stream); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// WriteStream consumes provider events without aggregating the complete answer.
// The caller owns the stream, its error and its close/finalization operations.
func (p SessionPresentation) WriteStream(out, diagnostics io.Writer, stream agentloop.Stream) error {
	if p.binary() {
		size, err := WriteBinaryModalityStream(out, stream, p.Modality)
		return p.checkBinary(diagnostics, size, err)
	}
	if !p.JSON {
		return WriteEventStreamToWriter(out, diagnostics, stream)
	}
	for stream.HasNext() {
		if err := p.writeJSONEvent(out, diagnostics, stream.Response()); err != nil {
			return err
		}
	}
	return nil
}

func (p SessionPresentation) writeJSONEvent(out, diagnostics io.Writer, event messages.StreamMessage) error {
	if event.Type != messages.StreamTypeRefusal {
		return WriteStreamEventJSON(out, event)
	}
	refusal, ok := event.Value.(*messages.RefusalValue)
	if !ok || refusal.Message == "" {
		return nil
	}
	return WriteRefusalJSON(diagnostics, refusal.Message, p.Model)
}

func (p SessionPresentation) writeRefusal(writer io.Writer, text string) error {
	if p.JSON {
		return WriteRefusalJSON(writer, text, p.Model)
	}
	return WriteRefusal(writer, text)
}

func (p SessionPresentation) checkBinary(diagnostics io.Writer, size int64, err error) error {
	if err != nil || size != 0 {
		return err
	}
	_, err = fmt.Fprintf(diagnostics, "no %s content in response\n", p.Modality)
	return err
}

// WriteSessionText writes one final text result, preserving streaming newlines.
func WriteSessionText(writer io.Writer, text string, streaming bool) error {
	var err error
	if streaming {
		_, err = fmt.Fprint(writer, text)
	} else {
		_, err = fmt.Fprintln(writer, text)
	}
	return err
}
