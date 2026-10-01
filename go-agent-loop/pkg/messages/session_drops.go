package messages

// BufferDropDirection names which side of an s2s session lost a message.
// Input is the client-to-provider send path; output is the provider-to-client
// receive path. The vocabulary is closed so log analysis cannot be split by
// spelling drift.
type BufferDropDirection string

const (
	// DropDirectionInput marks drops on the client-to-provider send path.
	DropDirectionInput BufferDropDirection = "input"
	// DropDirectionOutput marks drops on the provider-to-client receive path.
	DropDirectionOutput BufferDropDirection = "output"
)

// DropLogMessage is the single message string used for every buffer-drop
// record, so `grep "buffer drop"` finds every lost message across surfaces.
const DropLogMessage = "buffer drop"

// Field keys used by the default drop observer.
const (
	DropLogFieldDirection = "direction"
	DropLogFieldBuffer    = "buffer"
	DropLogFieldCount     = "count"
	DropLogFieldType      = "type"
)

// DropLogField is one structured metadata entry in a drop record. It is
// deliberately defined here, inside the shared contract package, so both
// go-agent-loop and go-llm-gateway loggers can adapt to it without importing
// each other's logging packages.
type DropLogField struct {
	Key   string
	Value any
}

// DropLogSink is the minimal structured-logging seam consumed by the default
// drop observer. Both the loop and gateway Logger interfaces satisfy it with
// a two-line adapter that converts their own field types.
type DropLogSink interface {
	Warn(msg string, fields ...DropLogField)
}

// SessionDropCounters is implemented by sessions whose input (send) and
// output (receive) paths drop into TypedBuffers, exposing the cumulative
// per-direction drop counts recorded over the session's lifetime. Probe and
// scenario runners read these figures so forced or observed overflow can be
// asserted directly instead of inferred from silence.
type SessionDropCounters interface {
	// InputDrops returns cumulative client-to-provider send-path drops.
	InputDrops() int64
	// OutputDrops returns cumulative provider-to-client receive-path drops.
	OutputDrops() int64
}

// AttachDefaultDropObserver registers the default structured drop observer on
// buf: exactly one Warn record per buffer-full drop carrying the direction,
// the owning buffer name, the cumulative drop count including this drop, and
// the dropped message's kind. A nil sink or nil buffer installs nothing;
// zero drops therefore emit zero records.
//
// kind may be nil when the element type has no meaningful kind; the type field
// is then logged as an empty string rather than skipping the record.
func AttachDefaultDropObserver[T any](sink DropLogSink, direction BufferDropDirection, bufferName string, buf *TypedBuffer[T], kind func(T) string) {
	if sink == nil || buf == nil {
		return
	}
	buf.SetOnDrop(func(dropped T) {
		kindName := ""
		if kind != nil {
			kindName = kind(dropped)
		}
		sink.Warn(
			DropLogMessage,
			DropLogField{Key: DropLogFieldDirection, Value: string(direction)},
			DropLogField{Key: DropLogFieldBuffer, Value: bufferName},
			DropLogField{Key: DropLogFieldCount, Value: buf.Drops()},
			DropLogField{Key: DropLogFieldType, Value: kindName},
		)
	})
}
