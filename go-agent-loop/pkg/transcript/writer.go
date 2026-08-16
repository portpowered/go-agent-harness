package transcript

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
)

const (
	// DefaultSegmentSize is the maximum size of an active transcript segment
	// before the next complete record is written to a new segment.
	DefaultSegmentSize int64 = 32 * 1024 * 1024
	// DefaultMaxBackups retains the active segment plus four rotated segments.
	DefaultMaxBackups = 4

	// Descriptive aliases for callers that prefer the configuration vocabulary.
	DefaultMaxSegmentBytes = DefaultSegmentSize
	DefaultBackupCount     = DefaultMaxBackups
)

var (
	// ErrWriterClosed is returned by writes after Close has completed.
	ErrWriterClosed = errors.New("transcript: writer closed")
	// ErrTranscriptDegraded identifies a sink failure without hiding its cause.
	ErrTranscriptDegraded = errors.New("transcript: recording degraded")
	// ErrInvalidWriterConfig identifies an invalid constructor argument.
	ErrInvalidWriterConfig = errors.New("transcript: invalid writer configuration")
)

// WriterState is the observable lifecycle/health state of a Writer.
type WriterState string

const (
	WriterHealthy  WriterState = "healthy"
	WriterDegraded WriterState = "degraded"
	WriterClosed   WriterState = "closed"

	// Short aliases keep status checks readable for callers that do not need the
	// Writer prefix.
	Healthy  = WriterHealthy
	Degraded = WriterDegraded
	Closed   = WriterClosed
)

// DegradedError records the first sink error that made recording unavailable.
// It is stable for the lifetime of a Writer and matches both the recording
// marker and the original sink cause with errors.Is.
type DegradedError struct {
	cause error
}

func (e *DegradedError) Error() string {
	if e == nil {
		return "transcript: recording degraded"
	}
	return fmt.Sprintf("%s: %v", ErrTranscriptDegraded, e.cause)
}

func (e *DegradedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(ErrTranscriptDegraded, e.cause)
}

// Cause returns the original sink error.
func (e *DegradedError) Cause() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// WriterConfig controls rolling, retention, and degradation reporting.
// SegmentSize and MaxBackups are the preferred fields. The MaxSegmentBytes
// and BackupCount names are accepted as equivalent aliases.
type WriterConfig struct {
	SegmentSize     int64
	MaxSegmentBytes int64
	MaxBackups      int
	BackupCount     int

	// Reporter is called once, after the Writer has entered degraded state.
	// The callback is never invoked while the Writer lock is held.
	Reporter   func(error)
	OnDegraded func(error)

	// Mode is used for newly created files. Zero uses 0644.
	Mode os.FileMode
}

// WriterOptions is a descriptive alias for WriterConfig.
type WriterOptions = WriterConfig

// WriterOption is an optional WriterConfig mutator for NewWriter.
type WriterOption func(*WriterConfig)

// DefaultWriterConfig returns the documented bounded-storage defaults.
func DefaultWriterConfig() WriterConfig {
	return WriterConfig{
		SegmentSize:     DefaultSegmentSize,
		MaxSegmentBytes: DefaultSegmentSize,
		MaxBackups:      DefaultMaxBackups,
		BackupCount:     DefaultMaxBackups,
		Mode:            0o644,
	}
}

// WithSegmentSize sets the active segment limit. Non-positive values are
// normalized back to DefaultSegmentSize by the constructor.
func WithSegmentSize(size int64) WriterOption {
	return func(config *WriterConfig) {
		config.SegmentSize = size
		config.MaxSegmentBytes = size
	}
}

// WithMaxSegmentBytes is an alias for WithSegmentSize.
func WithMaxSegmentBytes(size int64) WriterOption { return WithSegmentSize(size) }

// WithMaxBackups sets the number of rotated backup files. Non-positive values
// are normalized back to DefaultMaxBackups by the constructor.
func WithMaxBackups(backups int) WriterOption {
	return func(config *WriterConfig) {
		config.MaxBackups = backups
		config.BackupCount = backups
	}
}

// WithDegradationReporter installs the one-shot sink degradation callback.
func WithDegradationReporter(reporter func(error)) WriterOption {
	return func(config *WriterConfig) {
		config.Reporter = reporter
		config.OnDegraded = reporter
	}
}

// WithReporter is an alias for WithDegradationReporter.
func WithReporter(reporter func(error)) WriterOption {
	return WithDegradationReporter(reporter)
}

// WriterStatus is a race-safe snapshot of a Writer.
type WriterStatus struct {
	State    WriterState
	Err      error
	Accepted uint64
	Size     int64
}

type transcriptFile interface {
	io.Writer
	io.Closer
}

type syncer interface {
	Sync() error
}

// Writer serializes Record encoding, rolling, and complete-line writes to a
// bounded JSONL transcript. It owns the file opened by NewWriter.
type Writer struct {
	mu sync.Mutex

	path        string
	file        transcriptFile
	segmentSize int64
	maxBackups  int
	mode        os.FileMode
	reporter    func(error)

	size     int64
	accepted uint64
	state    WriterState

	degradedErr error
	closeErr    error
}

// NewWriter opens path for append and applies optional WriterOption values.
// The accepted record order is the order in which concurrent calls acquire
// the Writer's serialized write boundary.
func NewWriter(path string, options ...WriterOption) (*Writer, error) {
	config := WriterConfig{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	return NewWriterWithConfig(path, config)
}

// NewWriterWithConfig opens path using config. Zero and invalid size fields
// intentionally fall back to the bounded defaults rather than disabling
// rotation.
func NewWriterWithConfig(path string, config WriterConfig) (*Writer, error) {
	config = normalizeWriterConfig(config)
	if path == "" {
		return nil, fmt.Errorf("%w: empty path", ErrInvalidWriterConfig)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, config.Mode)
	if err != nil {
		return nil, fmt.Errorf("transcript: open %q: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("transcript: stat %q: %w", path, err)
	}

	return &Writer{
		path:        path,
		file:        file,
		segmentSize: config.SegmentSize,
		maxBackups:  config.MaxBackups,
		mode:        config.Mode,
		reporter:    writerReporter(config),
		size:        info.Size(),
		state:       WriterHealthy,
	}, nil
}

// NewWriterOn wraps an already-open sink. It is useful for non-filesystem
// sinks and failure injection; rolling is available only to path-backed
// Writers. The returned Writer owns and closes sink.
func NewWriterOn(sink io.WriteCloser, options ...WriterOption) (*Writer, error) {
	if sink == nil {
		return nil, fmt.Errorf("%w: nil sink", ErrInvalidWriterConfig)
	}
	config := WriterConfig{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	config = normalizeWriterConfig(config)
	return &Writer{
		file:        sink,
		segmentSize: config.SegmentSize,
		maxBackups:  config.MaxBackups,
		mode:        config.Mode,
		reporter:    writerReporter(config),
		state:       WriterHealthy,
	}, nil
}

// NewTranscriptWriter is a descriptive constructor alias.
func NewTranscriptWriter(path string, options ...WriterOption) (*Writer, error) {
	return NewWriter(path, options...)
}

// Append writes one complete encoded record and returns its one-based accepted
// sequence number. A failed write does not advance the sequence.
func (w *Writer) Append(record Record) (uint64, error) {
	if record.Version == 0 {
		return 0, ErrMissingVersion
	}
	if record.Version != FormatVersion {
		return 0, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedVersion, record.Version, FormatVersion)
	}
	encoded, err := Encode(record)
	if err != nil {
		return 0, fmt.Errorf("transcript: encode record: %w", err)
	}

	w.mu.Lock()
	if w.state == WriterClosed {
		err := ErrWriterClosed
		w.mu.Unlock()
		return 0, err
	}
	if w.degradedErr != nil {
		err := w.degradedErr
		w.mu.Unlock()
		return 0, err
	}
	if w.file == nil {
		reporter, err := w.degradeLocked(errors.New("transcript: sink unavailable"))
		w.mu.Unlock()
		notifyDegradation(reporter, err)
		return 0, err
	}

	if w.size > 0 && w.size+int64(len(encoded)) > w.segmentSize {
		if err := w.rotateLocked(); err != nil {
			reporter, degraded := w.degradeLocked(err)
			w.mu.Unlock()
			notifyDegradation(reporter, degraded)
			return 0, degraded
		}
	}

	n, writeErr := w.file.Write(encoded)
	if writeErr == nil && n != len(encoded) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil || n != len(encoded) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		reporter, degraded := w.degradeLocked(fmt.Errorf("transcript: write record: %w", writeErr))
		w.mu.Unlock()
		notifyDegradation(reporter, degraded)
		return 0, degraded
	}

	w.size += int64(n)
	w.accepted++
	sequence := w.accepted
	w.mu.Unlock()
	return sequence, nil
}

// Write appends one record and returns only its error, making Writer a simple
// RecordSink for Tee.
func (w *Writer) Write(record Record) error {
	_, err := w.Append(record)
	return err
}

// Flush synchronizes the active file without closing it.
func (w *Writer) Flush() error {
	w.mu.Lock()
	if w.state == WriterClosed {
		w.mu.Unlock()
		return ErrWriterClosed
	}
	if w.file == nil {
		w.mu.Unlock()
		return nil
	}
	syncable, ok := w.file.(syncer)
	if !ok {
		w.mu.Unlock()
		return nil
	}
	err := syncable.Sync()
	if err == nil {
		w.mu.Unlock()
		return nil
	}
	reporter, degraded := w.degradeLocked(fmt.Errorf("transcript: flush: %w", err))
	w.mu.Unlock()
	notifyDegradation(reporter, degraded)
	return degraded
}

// Close flushes and closes the owned sink. It is idempotent and returns the
// same close result on every call.
func (w *Writer) Close() error {
	w.mu.Lock()
	if w.state == WriterClosed {
		err := w.closeErr
		w.mu.Unlock()
		return err
	}

	var closeErr error
	if w.file != nil {
		file := w.file
		w.file = nil
		if syncable, ok := file.(syncer); ok {
			closeErr = syncable.Sync()
		}
		if err := file.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	w.closeErr = closeErr
	var reporter func(error)
	var reported error
	if closeErr != nil && w.degradedErr == nil {
		reporter, reported = w.degradeLocked(fmt.Errorf("transcript: close: %w", closeErr))
	}
	w.state = WriterClosed
	w.mu.Unlock()
	if reported != nil {
		notifyDegradation(reporter, reported)
	}
	return closeErr
}

// Status returns a consistent health, error, size, and accepted-count view.
func (w *Writer) Status() WriterStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return WriterStatus{State: w.state, Err: w.degradedErr, Accepted: w.accepted, Size: w.size}
}

// State returns the current WriterState.
func (w *Writer) State() WriterState { return w.Status().State }

// IsDegraded reports whether a sink error has been recorded.
func (w *Writer) IsDegraded() bool {
	w.mu.Lock()
	degraded := w.degradedErr != nil
	w.mu.Unlock()
	return degraded
}

// Degraded is an alias for IsDegraded.
func (w *Writer) Degraded() bool { return w.IsDegraded() }

// Err returns the first identifiable degradation error, if any.
func (w *Writer) Err() error {
	w.mu.Lock()
	err := w.degradedErr
	w.mu.Unlock()
	return err
}

// DegradationError is a descriptive alias for Err.
func (w *Writer) DegradationError() error { return w.Err() }

// AcceptedCount returns the number of complete records accepted by the sink.
func (w *Writer) AcceptedCount() uint64 { return w.Status().Accepted }

// SegmentSize returns the normalized active-segment limit.
func (w *Writer) SegmentSize() int64 { return w.segmentSize }

// MaxBackups returns the normalized retained-backup count.
func (w *Writer) MaxBackups() int { return w.maxBackups }

// BackupPath returns the path used for a one-based rotated backup.
func BackupPath(path string, backup int) string {
	return path + "." + strconv.Itoa(backup)
}

func normalizeWriterConfig(config WriterConfig) WriterConfig {
	segmentSize := config.SegmentSize
	if segmentSize <= 0 {
		segmentSize = config.MaxSegmentBytes
	}
	if segmentSize <= 0 {
		segmentSize = DefaultSegmentSize
	}
	backups := config.MaxBackups
	if backups <= 0 {
		backups = config.BackupCount
	}
	if backups <= 0 {
		backups = DefaultMaxBackups
	}
	if config.Mode == 0 {
		config.Mode = 0o644
	}
	config.SegmentSize = segmentSize
	config.MaxSegmentBytes = segmentSize
	config.MaxBackups = backups
	config.BackupCount = backups
	return config
}

func writerReporter(config WriterConfig) func(error) {
	if config.Reporter != nil {
		return config.Reporter
	}
	return config.OnDegraded
}

func (w *Writer) degradeLocked(cause error) (func(error), error) {
	if w.degradedErr != nil {
		return nil, w.degradedErr
	}
	if cause == nil {
		cause = errors.New("transcript: unknown sink failure")
	}
	w.degradedErr = &DegradedError{cause: cause}
	if w.state != WriterClosed {
		w.state = WriterDegraded
	}
	return w.reporter, w.degradedErr
}

func notifyDegradation(reporter func(error), err error) {
	if reporter == nil || err == nil {
		return
	}
	defer func() { _ = recover() }()
	reporter(err)
}

func (w *Writer) rotateLocked() error {
	if w.path == "" {
		return errors.New("transcript: sink cannot rotate")
	}
	if w.file == nil {
		return errors.New("transcript: sink unavailable during rotation")
	}

	file := w.file
	w.file = nil
	if syncable, ok := file.(syncer); ok {
		if err := syncable.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("transcript: sync before rotation: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("transcript: close before rotation: %w", err)
	}

	for backup := w.maxBackups; backup >= 1; backup-- {
		destination := BackupPath(w.path, backup)
		source := w.path
		if backup > 1 {
			source = BackupPath(w.path, backup-1)
		}
		if err := removeIfPresent(destination); err != nil {
			return fmt.Errorf("transcript: remove old backup %q: %w", destination, err)
		}
		if err := renameIfPresent(source, destination); err != nil {
			return fmt.Errorf("transcript: rotate %q to %q: %w", source, destination, err)
		}
	}

	newFile, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, w.mode)
	if err != nil {
		return fmt.Errorf("transcript: open new segment %q: %w", w.path, err)
	}
	w.file = newFile
	w.size = 0
	return nil
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func renameIfPresent(source, destination string) error {
	_, err := os.Stat(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.Rename(source, destination)
}
