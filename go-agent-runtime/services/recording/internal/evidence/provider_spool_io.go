package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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

const providerCaptureDigestHexLength = 64

// Provider and session metadata are public strings, so validate their lengths
// before encoding either object. This keeps the finalization scratch bounded
// even when a caller supplies an adversarial envelope that is much larger than
// the provider byte budget. The bounded fields preserve the public envelope
// shape; an oversized field fails closed instead of being silently truncated.
const providerCaptureMetadataFieldLimit = 4 << 10

func boundProviderCaptureMetadata(capture gatewaytesting.SessionCapture) (gatewaytesting.SessionCapture, error) {
	fields := []struct {
		name  string
		value string
	}{
		{name: "provider.name", value: capture.Provider.Name},
		{name: "provider.model", value: capture.Provider.Model},
		{name: "session.id", value: capture.Session.ID},
		{name: "session.started_at_utc", value: capture.Session.StartedAtUTC},
		{name: "session.fixture_provenance", value: capture.Session.FixtureProvenance},
	}
	for _, field := range fields {
		if len(field.value) > providerCaptureMetadataFieldLimit {
			return capture, fmt.Errorf("%w: provider capture metadata field %s exceeds %d bytes", errProviderCaptureBudget, field.name, providerCaptureMetadataFieldLimit)
		}
	}
	return capture, nil
}

func (s *providerCaptureSpool) checkEnvelopeBudget(capture gatewaytesting.SessionCapture) error {
	overhead, err := providerCaptureEnvelopeOverhead(capture)
	if err != nil {
		return fmt.Errorf("encode provider capture envelope metadata: %w", err)
	}
	s.mu.Lock()
	committedBytes := s.committedBytes
	limit := s.limits.ProviderBytes
	s.mu.Unlock()
	if overhead > limit-committedBytes {
		return errProviderCaptureBudget
	}
	return nil
}

func (s *providerCaptureSpool) updatePeaksLocked() {
	if s.queuedBytes > s.peakQueueBytes {
		s.peakQueueBytes = s.queuedBytes
	}
	if int64(s.queuedItems) > int64(s.peakQueueItems) {
		s.peakQueueItems = s.queuedItems
	}
	providerBytes := s.committedBytes + s.queuedBytes
	providerItems := s.committedItems + int64(s.queuedItems)
	if providerBytes > s.peakProviderBytes {
		s.peakProviderBytes = providerBytes
	}
	if providerItems > s.peakProviderItems {
		s.peakProviderItems = providerItems
	}
}

func providerCaptureEnvelopeOverhead(capture gatewaytesting.SessionCapture) (int64, error) {
	if _, err := boundProviderCaptureMetadata(capture); err != nil {
		return 0, err
	}
	version := capture.Version
	if version == 0 {
		version = gatewaytesting.SessionCaptureVersion
	}
	versionJSON, err := json.Marshal(version)
	if err != nil {
		return 0, err
	}
	providerJSON, err := json.Marshal(capture.Provider)
	if err != nil {
		return 0, err
	}
	sessionJSON, err := json.Marshal(capture.Session)
	if err != nil {
		return 0, err
	}
	integrityJSON, err := json.Marshal(gatewaytesting.SessionCaptureIntegrity{
		Algorithm: gatewaytesting.SessionCaptureIntegrityAlgorithm,
		Coverage:  gatewaytesting.SessionCaptureIntegrityCoverage,
		Digest:    strings.Repeat("0", providerCaptureDigestHexLength),
	})
	if err != nil {
		return 0, err
	}
	parts := [][]byte{
		[]byte(`{"version":`), versionJSON,
		[]byte(`,"provider":`), providerJSON,
		[]byte(`,"session":`), sessionJSON,
		[]byte(`,"records":[`), []byte(`]`),
		[]byte(`,"integrity":`), integrityJSON,
	}
	if capture.EndsWithDisconnect {
		parts = append(parts, []byte(`,"ends_with_disconnect":true`))
	}
	parts = append(parts, []byte(`}`))
	var total int64
	for _, part := range parts {
		total += int64(len(part))
	}
	return total, nil
}

func publishProviderCapture(path string, capture gatewaytesting.SessionCapture, reader gatewaytesting.SessionCaptureRecordReader) (returnErr error) {
	directory := filepath.Dir(path)
	base := filepath.Base(path)
	placeholder, err := os.CreateTemp(directory, "."+base+".provider-publish-")
	if err != nil {
		return fmt.Errorf("create provider capture publish path: %w", err)
	}
	stagePath := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		return errors.Join(fmt.Errorf("close provider capture publish path: %w", err), os.Remove(stagePath))
	}
	if err := os.Remove(stagePath); err != nil {
		return fmt.Errorf("reserve provider capture publish path: %w", err)
	}
	removeStage := true
	defer func() {
		if removeStage {
			if removeErr := os.Remove(stagePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove provider capture publish path: %w", removeErr))
			}
		}
	}()
	if err := gatewaytesting.WriteSessionCaptureFromReader(stagePath, capture, reader); err != nil {
		return err
	}
	if err := os.Link(stagePath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errProviderCaptureDestination
		}
		return fmt.Errorf("publish provider capture: %w", err)
	}
	if err := os.Remove(stagePath); err != nil {
		return fmt.Errorf("remove provider capture publish path: %w", err)
	}
	removeStage = false
	return nil
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
		s.processedItems++
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
	if providerCaptureCanDrain(s.currentError()) {
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
		if entry.state == providerCaptureCommit && providerCaptureCanDrain(s.currentError()) {
			if err := writeProviderCaptureLine(s.file, entry.encoded); err != nil {
				s.latch(fmt.Errorf("write provider capture spool: %w", err))
			} else {
				s.mu.Lock()
				s.committedBytes += entry.bytes
				s.committedItems++
				s.updatePeaksLocked()
				s.mu.Unlock()
			}
		}
		s.releaseBytes(entry.bytes)
		(*nextSequence)++
	}
}

func providerCaptureCanDrain(err error) bool {
	return err == nil ||
		errors.Is(err, errProviderCaptureBudget) ||
		errors.Is(err, errProviderCaptureQueueFull) ||
		errors.Is(err, errProviderCaptureEventTooLarge)
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
var _ recording.ResourceUsageReporter = (*providerCaptureSpool)(nil)
var _ gatewaytesting.SessionCaptureRecordReader = (*providerCaptureSpoolReader)(nil)
