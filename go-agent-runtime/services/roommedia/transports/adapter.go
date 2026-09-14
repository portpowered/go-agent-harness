// Package transports contains decision-free host adapters for roommedia
// ports. The media service itself remains in the private implementation.
package transports

import (
	"context"
	"errors"
	"io"
	"reflect"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia"
)

// AwaitGate waits for a start signal or either owner cancellation signal.
func AwaitGate(gate, firstDone, secondDone <-chan struct{}) bool {
	select {
	case <-gate:
		return true
	case <-firstDone:
	case <-secondDone:
	}
	return false
}

// AwaitValue waits for a typed readiness value or owner cancellation.
func AwaitValue[T any](values <-chan T, firstDone, secondDone <-chan struct{}) (T, bool) {
	select {
	case value := <-values:
		return value, true
	case <-firstDone:
	case <-secondDone:
	}
	var zero T
	return zero, false
}

// AwaitReadyNonNil combines a start gate and typed readiness channel.
func AwaitReadyNonNil[T comparable](gate <-chan struct{}, values <-chan T, firstDone, secondDone <-chan struct{}, unavailable func()) (T, bool) {
	if !AwaitGate(gate, firstDone, secondDone) {
		var zero T
		return zero, false
	}
	value, ok := AwaitValue(values, firstDone, secondDone)
	var zero T
	if !ok || value == zero {
		if unavailable != nil {
			unavailable()
		}
		return zero, false
	}
	return value, true
}

// IsNormalTermination identifies cancellation and end-of-stream results.
func IsNormalTermination(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Optional returns a callback only when the host installed that port.
func Optional[T any](enabled bool, value T) T {
	if enabled {
		return value
	}
	var zero T
	return zero
}

// IgnoreError turns a best-effort observation into a fire-and-forget callback.
func IgnoreError(observer func([]byte) error) func([]byte) {
	if observer == nil {
		return nil
	}
	return func(pcm []byte) {
		if err := observer(pcm); err != nil {
			return
		}
	}
}

// OnError invokes action only for an unhandled error.
func OnError(err error, handled bool, action func(error)) {
	if err != nil && !handled && action != nil {
		action(err)
	}
}

// HandleError runs an error action and returns its result.
func HandleError(err error, handled bool, action func(error) error) error {
	if err == nil || handled {
		return nil
	}
	if action == nil {
		return err
	}
	return action(err)
}

// ContextOr selects an explicit host context when present.
func ContextOr(primary, fallback context.Context) context.Context {
	if primary != nil {
		return primary
	}
	return fallback
}

// ReportError observes an error and returns its original identity.
func ReportError(err error, handled bool, action func(error)) error {
	OnError(err, handled, action)
	return err
}

// NewMixer adapts host-owned mixer callbacks to the roommedia port.
func NewMixer(format func() roommedia.PCM16Format, read func(context.Context) (roommedia.MixedFrame, error)) roommedia.Mixer {
	return roommedia.MixerFunc{FormatFunc: format, ReadFunc: read}
}

// StaticMixer exposes a format for operations that do not read frames.
func StaticMixer(format roommedia.PCM16Format) roommedia.Mixer {
	return roommedia.MixerFunc{FormatFunc: func() roommedia.PCM16Format { return format }}
}

// AdaptMixer converts a host mixer and frame type without importing it here.
func AdaptMixer[M any, F any](m interface {
	Format() F
	ReadFrameWithSources(context.Context) (M, error)
}, format func(F) roommedia.PCM16Format, frame func(M) roommedia.MixedFrame) roommedia.Mixer {
	return roommedia.MixerFunc{FormatFunc: func() roommedia.PCM16Format { return format(m.Format()) }, ReadFunc: func(ctx context.Context) (roommedia.MixedFrame, error) {
		value, err := m.ReadFrameWithSources(ctx)
		return frame(value), err
	}}
}

// AdaptMixerWithFormat converts a host frame type after its format is fixed.
func AdaptMixerWithFormat[M any](m interface {
	ReadFrameWithSources(context.Context) (M, error)
}, format roommedia.PCM16Format, frame func(any) roommedia.MixedFrame) roommedia.Mixer {
	return roommedia.MixerFunc{FormatFunc: func() roommedia.PCM16Format { return format }, ReadFunc: func(ctx context.Context) (roommedia.MixedFrame, error) {
		value, err := m.ReadFrameWithSources(ctx)
		return frame(value), err
	}}
}

// CopyPCM16Frame structurally copies host frames with PCM and Sources fields.
func CopyPCM16Frame(value any) roommedia.MixedFrame {
	frame := reflect.ValueOf(value)
	if frame.Kind() == reflect.Pointer {
		if frame.IsNil() {
			return roommedia.MixedFrame{}
		}
		frame = frame.Elem()
	}
	if !frame.IsValid() || frame.Kind() != reflect.Struct {
		return roommedia.MixedFrame{}
	}
	pcm, sources := frame.FieldByName("PCM"), frame.FieldByName("Sources")
	if !pcm.IsValid() || pcm.Type() != reflect.TypeOf([]byte(nil)) || !sources.IsValid() || sources.Type() != reflect.TypeOf([]string(nil)) {
		return roommedia.MixedFrame{}
	}
	pcmBytes := pcm.Bytes()
	sourceIDs := make([]string, sources.Len())
	for index := range sourceIDs {
		sourceIDs[index] = sources.Index(index).String()
	}
	return roommedia.MixedFrame{PCM: append([]byte(nil), pcmBytes...), Sources: append([]string(nil), sourceIDs...)}
}

// AdaptPolicySender bridges an equivalent string-backed host policy.
func AdaptPolicySender[P ~string](send func(context.Context, []byte, P) error) func(context.Context, []byte, roommedia.InputPolicy) error {
	return func(ctx context.Context, pcm []byte, policy roommedia.InputPolicy) error {
		return send(ctx, pcm, P(policy))
	}
}

// AdaptPolicy is the policy-side half of AdaptPolicySender.
func AdaptPolicy[P ~string](policy func([]string) P) func([]string) roommedia.InputPolicy {
	return func(sources []string) roommedia.InputPolicy { return roommedia.InputPolicy(policy(sources)) }
}

// AdaptInputHook retains participant identity and defensive-copy semantics.
func AdaptInputHook[P ~string](hook func(string, []byte) error, participantID string, fallback func(context.Context, []byte, P) error) func(context.Context, []byte, P) error {
	if hook == nil {
		return fallback
	}
	return func(_ context.Context, pcm []byte, _ P) error {
		return hook(participantID, append([]byte(nil), pcm...))
	}
}

// MapIf maps only values selected by the host adapter.
func MapIf[T any, R any](values []T, include func(T) bool, mapValue func(T) R) []R {
	result := make([]R, 0, len(values))
	for _, value := range values {
		if include == nil || include(value) {
			result = append(result, mapValue(value))
		}
	}
	return result
}

// MapNonZero maps comparable non-zero host values.
func MapNonZero[T comparable, R any](values []T, mapValue func(T) R) []R {
	var zero T
	result := make([]R, 0, len(values))
	for _, value := range values {
		if value != zero {
			result = append(result, mapValue(value))
		}
	}
	return result
}

// MapSupplier preserves a host's dynamic active-set lookup.
func MapSupplier[T comparable, R any](values func() []T, mapValue func(T) R) func() []R {
	return func() []R { return MapNonZero(values(), mapValue) }
}

// NewFanoutTarget constructs one immutable peer destination.
func NewFanoutTarget(id string, format roommedia.PCM16Format, active func() bool, write func(context.Context, string, []byte) error) roommedia.FanoutTarget {
	return roommedia.FanoutTarget{ID: id, Format: format, Active: active, Write: write}
}

// FanoutMapper lifts host callbacks into a roommedia target mapper.
func FanoutMapper[T any](id func(T) string, format func(T) roommedia.PCM16Format, active func(T) bool, write func(T, context.Context, string, []byte) error) func(T) roommedia.FanoutTarget {
	return func(value T) roommedia.FanoutTarget {
		return NewFanoutTarget(id(value), format(value), func() bool { return active(value) }, func(ctx context.Context, source string, pcm []byte) error { return write(value, ctx, source, pcm) })
	}
}
