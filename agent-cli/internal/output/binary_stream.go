package output

import (
	"fmt"
	"io"

	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// modalityStreamType maps an output modality name to the corresponding stream event type.
var modalityStreamType = map[string]messages.StreamMessageType{
	"image":     messages.StreamTypeImageDelta,
	"audio":     messages.StreamTypeAudioDelta,
	"video":     messages.StreamTypeVideoDelta,
	"embedding": messages.StreamTypeEmbeddingDelta,
}

// WriteBinaryModalityStream drains the event stream, writing only the raw bytes
// for the requested modality to w. All other event types are discarded.
// Returns the number of bytes written. If no matching content is found, returns 0.
func WriteBinaryModalityStream(w io.Writer, stream agentloop.Stream, modality string) (int64, error) {
	targetType, ok := modalityStreamType[modality]
	if !ok {
		return 0, fmt.Errorf("unsupported binary output modality: %s", modality)
	}

	var total int64
	for stream.HasNext() {
		evt := stream.Response()
		if evt.Type != targetType {
			continue
		}
		data := extractDeltaBytes(evt)
		if len(data) == 0 {
			continue
		}
		n, err := w.Write(data)
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// WriteBinaryModalityMessages extracts binary content from result messages for
// the given modality and writes raw bytes to w. Used for non-streaming mode.
// Returns the number of bytes written.
func WriteBinaryModalityMessages(w io.Writer, msgs []messages.Message, modality string) (int64, error) {
	var total int64
	for _, m := range msgs {
		for _, p := range m.ContentParts {
			data := extractPartBytes(p, modality)
			if len(data) == 0 {
				continue
			}
			n, err := w.Write(data)
			total += int64(n)
			if err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

// extractDeltaBytes returns the raw byte content from a stream delta event value.
func extractDeltaBytes(evt messages.StreamMessage) []byte {
	switch v := evt.Value.(type) {
	case *messages.ImageDeltaValue:
		return v.Content
	case *messages.AudioDeltaValue:
		return v.Content
	case *messages.VideoDeltaValue:
		return v.Content
	case *messages.EmbeddingDeltaValue:
		return v.Content
	default:
		return nil
	}
}

// extractPartBytes returns the raw byte content from a content part matching the modality.
func extractPartBytes(p messages.ContentPart, modality string) []byte {
	switch modality {
	case "image":
		if ip, ok := p.(messages.ImagePart); ok {
			return ip.Bytes
		}
	case "audio":
		if ap, ok := p.(messages.AudioPart); ok {
			return ap.Bytes
		}
	case "video":
		if vp, ok := p.(messages.VideoPart); ok {
			return vp.Bytes
		}
	case "embedding":
		if ep, ok := p.(messages.EmbeddingPart); ok {
			return ep.Bytes
		}
	}
	return nil
}
