package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func (s *providerCaptureSpool) removeSpool() error {
	err := os.Remove(s.spoolPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove provider capture spool: %w", err)
	}
	return nil
}

type providerCaptureSpoolReader struct {
	decoder *json.Decoder
}

func (r *providerCaptureSpoolReader) Next() (gatewaytesting.CapturedSessionEvent, bool, error) {
	var event gatewaytesting.CapturedSessionEvent
	if err := r.decoder.Decode(&event); err != nil {
		if errors.Is(err, io.EOF) {
			return gatewaytesting.CapturedSessionEvent{}, false, nil
		}
		return gatewaytesting.CapturedSessionEvent{}, false, err
	}
	return event, true, nil
}

func writeProviderCaptureLine(file *os.File, encoded []byte) error {
	if err := writeAll(file, encoded); err != nil {
		return err
	}
	return writeAll(file, []byte{'\n'})
}

func encodeProviderCaptureEvent(event gatewaytesting.CapturedSessionEvent) ([]byte, error) {
	return json.Marshal(event)
}

func sameCapturePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func (s *providerCaptureSpool) run() {
	defer close(s.done)
	pending := make(map[int]providerCapturePending)
	nextSequence := 0
	for {
		mutation, ok := <-s.queue
		if !ok {
			break
		}
		s.mu.Lock()
		if s.queueItems > 0 {
			s.queueItems--
		}
		s.mu.Unlock()
		s.applyMutation(pending, &nextSequence, mutation)
		s.drainPending(pending, &nextSequence)
	}
	if len(pending) > 0 {
		s.latch(errProviderCaptureUnresolved)
	}
	if s.currentError() == nil {
		if err := s.file.Sync(); err != nil {
			s.latch(fmt.Errorf("sync provider capture spool: %w", err))
		}
	}
	if err := s.file.Close(); err != nil {
		s.latch(fmt.Errorf("close provider capture spool: %w", err))
	}
}

func (s *providerCaptureSpool) applyMutation(pending map[int]providerCapturePending, nextSequence *int, mutation providerCaptureMutation) {
	switch mutation.kind {
	case providerCaptureAppend:
		if mutation.sequence <= 0 || (*nextSequence != 0 && mutation.sequence < *nextSequence) {
			s.releaseBytes(mutation.bytes)
			s.latch(errProviderCaptureUnresolved)
			return
		}
		if _, exists := pending[mutation.sequence]; exists {
			s.releaseBytes(mutation.bytes)
			s.latch(errProviderCaptureUnresolved)
			return
		}
		if *nextSequence == 0 {
			*nextSequence = mutation.sequence
		}
		pending[mutation.sequence] = providerCapturePending{encoded: mutation.encoded, state: providerCaptureAppend, bytes: mutation.bytes}
	case providerCaptureCommit, providerCaptureDiscard:
		entry, ok := pending[mutation.sequence]
		if !ok {
			s.latch(errProviderCaptureUnresolved)
			return
		}
		if entry.state != providerCaptureAppend {
			s.latch(errProviderCaptureUnresolved)
			return
		}
		entry.state = mutation.kind
		pending[mutation.sequence] = entry
	default:
		s.latch(errProviderCaptureUnresolved)
	}
}

func (s *providerCaptureSpool) drainPending(pending map[int]providerCapturePending, nextSequence *int) {
	for *nextSequence != 0 {
		entry, ok := pending[*nextSequence]
		if !ok || entry.state == providerCaptureAppend {
			return
		}
		delete(pending, *nextSequence)
		if entry.state == providerCaptureCommit && s.currentError() == nil {
			if err := writeProviderCaptureLine(s.file, entry.encoded); err != nil {
				s.latch(fmt.Errorf("write provider capture spool: %w", err))
			} else {
				s.mu.Lock()
				s.committedBytes += entry.bytes
				s.committedItems++
				s.mu.Unlock()
			}
		}
		s.releaseBytes(entry.bytes)
		(*nextSequence)++
	}
}

func (s *providerCaptureSpool) releaseBytes(bytes int64) {
	s.mu.Lock()
	s.releaseLocked(bytes)
	s.mu.Unlock()
}

func (s *providerCaptureSpool) releaseLocked(bytes int64) {
	s.queuedBytes -= bytes
	if s.queuedItems > 0 {
		s.queuedItems--
	}
}

var _ recording.ProviderCaptureSink = (*providerCaptureSpool)(nil)
var _ gatewaytesting.SessionCaptureRecordReader = (*providerCaptureSpoolReader)(nil)
