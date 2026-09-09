package evidence

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	providerCaptureQueueCapacity  = 256
	providerCaptureControlReserve = 16
	providerCaptureQueueMaxBytes  = 16 << 20
	providerCaptureQueueMaxItems  = 4096
	providerCaptureMaxEventBytes  = 4 << 20
)

type providerCaptureError string

func (e providerCaptureError) Error() string { return string(e) }

func (e providerCaptureError) Is(target error) bool {
	return e == errProviderCaptureBudget && target == io.ErrShortBuffer
}

const (
	errProviderCaptureClosed        providerCaptureError = "provider capture is closed"
	errProviderCaptureQueueFull     providerCaptureError = "provider capture queue is full"
	errProviderCaptureBudget        providerCaptureError = "provider capture cumulative budget exceeded"
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
	encoded  []byte
	bytes    int64
}

type providerCapturePending struct {
	encoded []byte
	state   providerCaptureMutationKind
	bytes   int64
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
	limits      recording.ResourceLimits

	mu             sync.Mutex
	queuedBytes    int64
	queuedItems    int
	queueItems     int
	committedBytes int64
	committedItems int64
	closed         bool
	err            error

	finishOnce sync.Once
	finishErr  error
}

// NewProviderCapture creates a bounded raw provider capture sink. The
// destination directory must already exist; host composition owns directory
// creation and path policy before admission.
func NewProviderCapture(destination string) (sink recording.ProviderCaptureSink, returnErr error) {
	return NewProviderCaptureWithLimits(destination, recording.ResourceLimits{})
}

func NewProviderCaptureWithLimits(destination string, input recording.ResourceLimits) (sink recording.ProviderCaptureSink, returnErr error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return nil, errors.New("provider capture destination is required")
	}
	budget, err := newEvidenceResourceBudget(input)
	if err != nil {
		return nil, err
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
		limits:      budget.limits,
	}
	go spool.run()
	remove = false
	return spool, nil
}

func (s *providerCaptureSpool) Append(event gatewaytesting.CapturedSessionEvent) error {
	if s == nil {
		return errProviderCaptureClosed
	}
	if int64(len(event.Payload)+len(event.Data)) > providerCaptureMaxEventBytes {
		s.latch(errProviderCaptureEventTooLarge)
		return errProviderCaptureEventTooLarge
	}
	encoded, err := encodeProviderCaptureEvent(event)
	if err != nil {
		s.latch(fmt.Errorf("encode provider capture event: %w", err))
		return err
	}
	bytes := int64(len(encoded) + 1)
	if bytes > providerCaptureMaxEventBytes {
		s.latch(errProviderCaptureEventTooLarge)
		return errProviderCaptureEventTooLarge
	}
	if err := s.reserve(bytes); err != nil {
		return err
	}
	return s.enqueueReserved(providerCaptureMutation{kind: providerCaptureAppend, sequence: event.Sequence, encoded: encoded, bytes: bytes})
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
	if bytes > s.limits.ProviderBytes-s.committedBytes-s.queuedBytes || s.committedItems+int64(s.queuedItems) >= s.limits.ProviderItems {
		s.latchLocked(errProviderCaptureBudget)
		return errProviderCaptureBudget
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
	if s.queueItems >= providerCaptureQueueCapacity-providerCaptureControlReserve {
		s.releaseLocked(mutation.bytes)
		s.latchLocked(errProviderCaptureQueueFull)
		return errProviderCaptureQueueFull
	}
	select {
	case s.queue <- mutation:
		s.queueItems++
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
	if s.queueItems >= providerCaptureQueueCapacity {
		s.latchLocked(errProviderCaptureQueueFull)
		return errProviderCaptureQueueFull
	}
	select {
	case s.queue <- providerCaptureMutation{kind: kind, sequence: sequence}:
		s.queueItems++
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
		return errors.Join(s.prefixDiagnostic(err), s.removeSpool())
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

func (s *providerCaptureSpool) prefixDiagnostic(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.committedItems == 0 {
		return err
	}
	return fmt.Errorf("%w: accepted provider prefix items=%d bytes=%d", err, s.committedItems, s.committedBytes)
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
