package service

import (
	"context"
	"io"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type cancellationIntent struct{ sigint atomic.Bool }

func NewCancellationIntent() sessiontrace.CancellationIntent {
	return &cancellationIntent{}
}

func (i *cancellationIntent) MarkSIGINT() {
	if i != nil {
		i.sigint.Store(true)
	}
}

func (i *cancellationIntent) SIGINTReceived() bool {
	return i != nil && i.sigint.Load()
}

func wrapAudioSource(source audio.AudioSource, rate int, observer sessiontrace.CaptureSamplesObserver) audio.AudioSource {
	if source == nil || observer == nil {
		return source
	}
	if rate <= 0 {
		rate = audio.SampleRate
	}
	if sampleSource, ok := source.(audio.SampleSource); ok {
		return &traceSampleSource{source: sampleSource, rate: rate, observer: observer}
	}
	return &traceAudioSource{source: source, rate: rate, observer: observer}
}

type traceAudioSource struct {
	source   audio.AudioSource
	rate     int
	observer sessiontrace.CaptureSamplesObserver
}

func (s *traceAudioSource) ReadFrame(ctx context.Context, buf []int16) error {
	if s == nil || s.source == nil {
		return io.EOF
	}
	if err := s.source.ReadFrame(ctx, buf); err != nil {
		return err
	}
	if len(buf) > 0 && s.observer != nil {
		s.observer(s.rate, append([]int16(nil), buf...))
	}
	return nil
}

func (s *traceAudioSource) Close() error {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source.Close()
}

type traceSampleSource struct {
	source   audio.SampleSource
	rate     int
	observer sessiontrace.CaptureSamplesObserver
}

func (s *traceSampleSource) ReadFrame(ctx context.Context, buf []int16) error {
	if s == nil || s.source == nil {
		return io.EOF
	}
	if err := s.source.ReadFrame(ctx, buf); err != nil {
		return err
	}
	if len(buf) > 0 && s.observer != nil {
		s.observer(s.rate, append([]int16(nil), buf...))
	}
	return nil
}

func (s *traceSampleSource) ReadSamples(ctx context.Context, buf []int16) (int, error) {
	if s == nil || s.source == nil {
		return 0, io.EOF
	}
	count, err := s.source.ReadSamples(ctx, buf)
	if count > 0 && count <= len(buf) && s.observer != nil {
		s.observer(s.rate, append([]int16(nil), buf[:count]...))
	}
	return count, err
}

func (s *traceSampleSource) Close() error {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source.Close()
}

var _ audio.AudioSource = (*traceAudioSource)(nil)
var _ audio.SampleSource = (*traceSampleSource)(nil)

// NewUnresolvedToolResultsError returns the typed diagnostic for provider
// calls whose local results never reached the provider. IDs are trimmed,
// de-duplicated and sorted; statuses are retained only for listed calls.
func NewUnresolvedToolResultsError(ids []string, statuses map[string]messages.SessionSendStatus) *sessiontrace.UnresolvedToolResultsError {
	seen := make(map[string]struct{}, len(ids))
	ordered := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	owned := make(map[string]messages.SessionSendStatus, len(statuses))
	for _, id := range ordered {
		if status, ok := statuses[id]; ok {
			owned[id] = status
		}
	}
	return &sessiontrace.UnresolvedToolResultsError{CallIDs: ordered, SendStatuses: owned}
}
