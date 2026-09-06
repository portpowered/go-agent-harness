package evidence

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	providerCaptureQueueCapacity = 256
	providerCaptureQueueMaxBytes = 16 << 20
	providerCaptureQueueMaxItems = 4096
	providerCaptureMaxEventBytes = 4 << 20
	providerCaptureEventOverhead = 128
)

type providerCaptureError string

func (e providerCaptureError) Error() string { return string(e) }

const (
	errProviderCaptureClosed        providerCaptureError = "provider capture is closed"
	errProviderCaptureQueueFull     providerCaptureError = "provider capture queue is full"
	errProviderCaptureEventTooLarge providerCaptureError = "provider capture event is too large"
	errProviderCaptureDestination   providerCaptureError = "provider capture destination changed"
	errProviderCaptureUnresolved    providerCaptureError = "provider capture has unsettled events"
)

type providerCaptureMutationKind uint8

const (
	providerCaptureAppend providerCaptureMutationKind = iota + 1
	providerCaptureCommit
	providerCaptureDiscard
)

type providerCaptureMutation struct {
	kind     providerCaptureMutationKind
	sequence int
	event    gatewaytesting.CapturedSessionEvent
	bytes    int64
}

type providerCapturePending struct {
	event gatewaytesting.CapturedSessionEvent
	state providerCaptureMutationKind
	bytes int64
}

// providerCaptureSpool owns the only provider-capture queue and file writer.
// Admission never performs filesystem work; the worker drains accepted
// mutations in order and the finalizer streams the spool into the protected
// gateway envelope.
type providerCaptureSpool struct {
	destination string
	spoolPath   string
	file        *os.File
	queue       chan providerCaptureMutation
	done        chan struct{}

	mu          sync.Mutex
	queuedBytes int64
	queuedItems int
	closed      bool
	err         error

	finishOnce sync.Once
	finishErr  error
}

// NewProviderCapture creates a bounded raw provider capture sink. The
// destination directory must already exist; host composition owns directory
// creation and path policy before admission.
func NewProviderCapture(destination string) (sink recording.ProviderCaptureSink, returnErr error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return nil, errors.New("provider capture destination is required")
	}
	directory := filepath.Dir(destination)
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect provider capture directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("provider capture destination directory is not a directory")
	}
	base := filepath.Base(destination)
	file, err := os.CreateTemp(directory, "."+base+".provider-spool-")
	if err != nil {
		return nil, fmt.Errorf("create provider capture spool: %w", err)
	}
	remove := true
	defer func() {
		if remove {
			returnErr = errors.Join(returnErr, file.Close(), os.Remove(file.Name()))
		}
	}()
	if err := file.Chmod(evidenceFileMode); err != nil {
		return nil, fmt.Errorf("protect provider capture spool: %w", err)
	}
	spool := &providerCaptureSpool{
		destination: destination,
		spoolPath:   file.Name(),
		file:        file,
		queue:       make(chan providerCaptureMutation, providerCaptureQueueCapacity),
		done:        make(chan struct{}),
	}
	go spool.run()
	remove = false
	return spool, nil
}

func (s *providerCaptureSpool) Append(event gatewaytesting.CapturedSessionEvent) error {
	if s == nil {
		return errProviderCaptureClosed
	}
	bytes := providerCaptureEventBytes(event)
	if bytes > providerCaptureMaxEventBytes {
		s.latch(errProviderCaptureEventTooLarge)
		return errProviderCaptureEventTooLarge
	}
	if err := s.reserve(bytes); err != nil {
		return err
	}
	event = cloneProviderCaptureEvent(event)
	return s.enqueueReserved(providerCaptureMutation{kind: providerCaptureAppend, sequence: event.Sequence, event: event, bytes: bytes})
}

func (s *providerCaptureSpool) Commit(sequence int) error {
	if s == nil {
		return errProviderCaptureClosed
	}
	return s.admitControl(providerCaptureCommit, sequence)
}

func (s *providerCaptureSpool) Discard(sequence int) error {
	if s == nil {
		return errProviderCaptureClosed
	}
	return s.admitControl(providerCaptureDiscard, sequence)
}

func (s *providerCaptureSpool) reserve(bytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errProviderCaptureClosed
	}
	if s.err != nil {
		return s.err
	}
	if bytes <= 0 || bytes > providerCaptureQueueMaxBytes || s.queuedBytes > providerCaptureQueueMaxBytes-bytes || s.queuedItems >= providerCaptureQueueMaxItems {
		s.latchLocked(errProviderCaptureQueueFull)
		return errProviderCaptureQueueFull
	}
	s.queuedBytes += bytes
	s.queuedItems++
	return nil
}

func (s *providerCaptureSpool) enqueueReserved(mutation providerCaptureMutation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.releaseLocked(mutation.bytes)
		return errProviderCaptureClosed
	}
	if s.err != nil {
		s.releaseLocked(mutation.bytes)
		return s.err
	}
	select {
	case s.queue <- mutation:
		return nil
	default:
		s.releaseLocked(mutation.bytes)
		s.latchLocked(errProviderCaptureQueueFull)
		return errProviderCaptureQueueFull
	}
}

func (s *providerCaptureSpool) admitControl(kind providerCaptureMutationKind, sequence int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errProviderCaptureClosed
	}
	if s.err != nil {
		return s.err
	}
	select {
	case s.queue <- providerCaptureMutation{kind: kind, sequence: sequence}:
		return nil
	default:
		s.latchLocked(errProviderCaptureQueueFull)
		return errProviderCaptureQueueFull
	}
}

func (s *providerCaptureSpool) FlushToFile(path string, capture gatewaytesting.SessionCapture) error {
	if s == nil {
		return errProviderCaptureClosed
	}
	s.finishOnce.Do(func() { s.finishErr = s.flush(path, capture) })
	return s.finishErr
}

func (s *providerCaptureSpool) Abort() error {
	if s == nil {
		return nil
	}
	s.finishOnce.Do(func() { s.finishErr = s.abort() })
	return s.finishErr
}

func (s *providerCaptureSpool) flush(path string, capture gatewaytesting.SessionCapture) error {
	if !sameCapturePath(path, s.destination) {
		return errors.Join(errProviderCaptureDestination, s.abort())
	}
	s.closeAdmission()
	<-s.done
	if err := s.currentError(); err != nil {
		return errors.Join(err, s.removeSpool())
	}
	file, err := os.Open(s.spoolPath)
	if err != nil {
		return errors.Join(fmt.Errorf("open provider capture spool for finalization: %w", err), s.removeSpool())
	}
	reader := &providerCaptureSpoolReader{decoder: json.NewDecoder(bufio.NewReader(file))}
	writeErr := gatewaytesting.WriteSessionCaptureFromReader(path, capture, reader)
	closeErr := file.Close()
	removeErr := s.removeSpool()
	return errors.Join(writeErr, closeErr, removeErr)
}

func (s *providerCaptureSpool) abort() error {
	s.closeAdmission()
	<-s.done
	return s.removeSpool()
}

func (s *providerCaptureSpool) closeAdmission() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.mu.Unlock()
}

func (s *providerCaptureSpool) currentError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *providerCaptureSpool) latch(err error) {
	s.mu.Lock()
	s.latchLocked(err)
	s.mu.Unlock()
}

func (s *providerCaptureSpool) latchLocked(err error) {
	if s.err == nil && err != nil {
		s.err = err
	}
}

func (s *providerCaptureSpool) run() {
	defer close(s.done)
	pending := make(map[int]providerCapturePending)
	nextSequence := 0
	for mutation := range s.queue {
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
		pending[mutation.sequence] = providerCapturePending{event: mutation.event, state: providerCaptureAppend, bytes: mutation.bytes}
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
			encoded, err := json.Marshal(entry.event)
			if err != nil {
				s.latch(fmt.Errorf("encode provider capture event: %w", err))
			} else if err := writeProviderCaptureLine(s.file, encoded); err != nil {
				s.latch(fmt.Errorf("write provider capture spool: %w", err))
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
