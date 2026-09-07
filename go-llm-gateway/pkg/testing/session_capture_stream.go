package testing

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	legacyCaptureFileMode    = 0o644
	protectedCaptureFileMode = 0o600
)

// SessionCaptureSink is the optional bounded persistence port used by a
// recording WebSocket dialer. Append reserves an event, while Commit or
// Discard settles that reservation after the wrapped transport returns.
// These methods only admit bounded mutations; they do not perform filesystem
// work on the provider goroutine. FlushToFile publishes one verified capture
// after all admitted mutations are drained. Abort drops a failed or
// unstarted capture without publishing a partial file.
type SessionCaptureSink interface {
	Append(CapturedSessionEvent) error
	Commit(sequence int) error
	Discard(sequence int) error
	FlushToFile(path string, capture SessionCapture) error
	Abort() error
}

// SessionCaptureRecordReader supplies accepted records in sequence order to
// the streaming envelope writer. The reader may omit reserved records that
// were discarded after a failed outbound write.
type SessionCaptureRecordReader interface {
	Next() (CapturedSessionEvent, bool, error)
}

// WriteSessionCaptureFromReader writes a current protected capture without
// materializing its record slice. The output is assembled in a mode-0600
// sibling temporary file and renamed only after the integrity digest and all
// writes succeed.
func WriteSessionCaptureFromReader(path string, capture SessionCapture, reader SessionCaptureRecordReader) (returnErr error) {
	if reader == nil {
		return errors.New("session capture record reader is required")
	}
	if capture.Version == 0 {
		capture.Version = SessionCaptureVersion
	}
	if capture.Version != SessionCaptureVersion {
		return newSessionCaptureValidationError(
			path,
			SessionCaptureErrorClassUnsupportedVersion,
			"/version",
			0,
			"",
			fmt.Sprintf("%d", SessionCaptureVersion),
			fmt.Sprintf("%d", capture.Version),
			ErrSessionCaptureUnsupportedVersion,
		)
	}
	temporary, err := newCaptureTemporary(path)
	if err != nil {
		return err
	}
	removeTemporary := true
	temporaryClosed := false
	defer func() {
		var closeErr error
		if !temporaryClosed {
			closeErr = temporary.Close()
		}
		if removeTemporary {
			removeErr := os.Remove(temporary.Name())
			returnErr = errors.Join(returnErr, closeErr, removeErr)
			return
		}
		returnErr = errors.Join(returnErr, closeErr)
	}()

	digest := sha256.New()
	coverage := io.MultiWriter(temporary, digest)
	if err := writeCaptureCoverageHeader(coverage, capture); err != nil {
		return err
	}
	if err := writeCaptureRecords(coverage, reader, path); err != nil {
		return err
	}
	integrity, err := captureStreamIntegrity(digest, capture.EndsWithDisconnect)
	if err != nil {
		return err
	}
	if err := writeCaptureEnvelopeFooter(temporary, integrity, capture.EndsWithDisconnect); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync session capture: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close session capture: %w", err)
	}
	temporaryClosed = true
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("publish session capture: %w", err)
	}
	removeTemporary = false
	return nil
}

func writeCaptureRecords(writer io.Writer, reader SessionCaptureRecordReader, path string) error {
	previousSequence := 0
	outputIndex := 0
	for {
		record, ok, err := readCaptureRecord(reader)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if err := writeCaptureRecord(writer, path, outputIndex, previousSequence, record); err != nil {
			return err
		}
		previousSequence = record.Sequence
		outputIndex++
	}
	if _, err := io.WriteString(writer, "]"); err != nil {
		return fmt.Errorf("write session capture records close: %w", err)
	}
	return nil
}

func readCaptureRecord(reader SessionCaptureRecordReader) (CapturedSessionEvent, bool, error) {
	record, ok, err := reader.Next()
	if err != nil {
		return CapturedSessionEvent{}, false, fmt.Errorf("read session capture spool: %w", err)
	}
	return record, ok, nil
}

func writeCaptureRecord(writer io.Writer, path string, index, previousSequence int, record CapturedSessionEvent) error {
	if err := validateSessionCaptureRecord(path, index, record, previousSequence); err != nil {
		return err
	}
	if index > 0 {
		if _, err := io.WriteString(writer, ","); err != nil {
			return fmt.Errorf("write session capture separator: %w", err)
		}
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode session capture record %d: %w", record.Sequence, err)
	}
	if _, err := writer.Write(encoded); err != nil {
		return fmt.Errorf("write session capture record %d: %w", record.Sequence, err)
	}
	return nil
}

func captureStreamIntegrity(digest hash.Hash, endsWithDisconnect bool) (SessionCaptureIntegrity, error) {
	if endsWithDisconnect {
		if _, err := io.WriteString(digest, `,"ends_with_disconnect":true`); err != nil {
			return SessionCaptureIntegrity{}, fmt.Errorf("write session capture digest: %w", err)
		}
	}
	if _, err := io.WriteString(digest, "}"); err != nil {
		return SessionCaptureIntegrity{}, fmt.Errorf("write session capture digest: %w", err)
	}
	return SessionCaptureIntegrity{
		Algorithm: SessionCaptureIntegrityAlgorithm,
		Coverage:  SessionCaptureIntegrityCoverage,
		Digest:    fmt.Sprintf("%x", digest.Sum(nil)),
	}, nil
}

func writeCaptureEnvelopeFooter(writer io.Writer, integrity SessionCaptureIntegrity, endsWithDisconnect bool) error {
	encodedIntegrity, err := json.Marshal(integrity)
	if err != nil {
		return fmt.Errorf("encode session capture integrity: %w", err)
	}
	if _, err := io.WriteString(writer, `,"integrity":`); err != nil {
		return fmt.Errorf("write session capture integrity field: %w", err)
	}
	if _, err := writer.Write(encodedIntegrity); err != nil {
		return fmt.Errorf("write session capture integrity: %w", err)
	}
	if endsWithDisconnect {
		if _, err := io.WriteString(writer, `,"ends_with_disconnect":true`); err != nil {
			return fmt.Errorf("write session capture disconnect marker: %w", err)
		}
	}
	if _, err := io.WriteString(writer, "}"); err != nil {
		return fmt.Errorf("write session capture close: %w", err)
	}
	return nil
}

// StreamingRecordingWebSocketDialer exposes only transport and finalization
// operations for a bounded capture. It intentionally has no Capture method:
// records live in the sink and must not be represented as a sealed empty
// in-memory snapshot.
type StreamingRecordingWebSocketDialer struct {
	dialer *RecordingWebSocketDialer
}

var _ transport.Dialer = (*StreamingRecordingWebSocketDialer)(nil)

// NewRecordingWebSocketDialerWithSink wraps a live WebSocket dialer with a
// bounded capture sink. An accepted Append must copy the event before it
// returns; the provider path may otherwise reuse its payload buffer. It must
// keep Append, Commit, and Discard admission non-blocking. The
// existing constructor remains the in-memory fixture path for compatibility.
func NewRecordingWebSocketDialerWithSink(inner transport.Dialer, providerName, model string, sink SessionCaptureSink, sources ...clock.Source) (*StreamingRecordingWebSocketDialer, error) {
	if sink == nil {
		return nil, errors.New("streaming recording dialer requires a capture sink")
	}
	d := NewRecordingWebSocketDialer(inner, providerName, model, sources...)
	d.sink = sink
	return &StreamingRecordingWebSocketDialer{dialer: d}, nil
}

func (d *StreamingRecordingWebSocketDialer) Dial(url string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.dialer == nil {
		return nil, errors.New("streaming recording dialer is unavailable")
	}
	return d.dialer.Dial(url, headers)
}

func (d *StreamingRecordingWebSocketDialer) FlushToFile(path string) error {
	if d == nil || d.dialer == nil {
		return errors.New("streaming recording dialer is unavailable")
	}
	return d.dialer.FlushToFile(path)
}
func (d *RecordingWebSocketDialer) FlushToFile(path string) error {
	d.mu.Lock()
	sink, sinkErr := d.sink, d.sinkErr
	capture := d.capture
	d.mu.Unlock()
	if sink != nil {
		if sinkErr != nil {
			return errors.Join(sinkErr, sink.Abort())
		}
		if err := sink.FlushToFile(path, capture); err != nil {
			return errors.Join(fmt.Errorf("flush session capture sink: %w", err), sink.Abort())
		}
		return nil
	}
	capture = d.Capture()
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session captures: %w", err)
	}
	if err := os.WriteFile(path, data, legacyCaptureFileMode); err != nil {
		return fmt.Errorf("write session capture file: %w", err)
	}
	return nil
}

func (d *RecordingWebSocketDialer) recordMessage(dir SessionEventDirection, payload []byte) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.sequence++
	sequence := d.sequence
	if d.sink != nil && d.sinkErr != nil {
		return sequence
	}
	event := CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   dir,
		TimestampMs: d.clock.Now().Sub(d.startAt).Milliseconds(),
		Type:        websocketPayloadType(payload),
		PayloadType: SessionPayloadTypeWebSocketMessage,
	}
	if d.sink != nil {
		event.Payload = payload
		if err := d.sink.Append(event); err != nil && d.sinkErr == nil {
			d.sinkErr = err
		}
		return sequence
	}
	event.Payload = append([]byte(nil), payload...)
	d.events = append(d.events, event)
	return sequence
}

func (d *RecordingWebSocketDialer) commitMessage(sequence int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sink == nil {
		return
	}
	if d.sinkErr != nil {
		return
	}
	if err := d.sink.Commit(sequence); err != nil && d.sinkErr == nil {
		d.sinkErr = err
	}
}

func (d *RecordingWebSocketDialer) discardMessage(sequence int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sink != nil {
		if d.sinkErr != nil {
			return
		}
		if err := d.sink.Discard(sequence); err != nil && d.sinkErr == nil {
			d.sinkErr = err
		}
		return
	}

	for index := range d.events {
		if d.events[index].Sequence != sequence {
			continue
		}
		d.events[index].PayloadType = ""
		d.events[index].Payload = nil
		return
	}
}

type recordingWebSocketConn struct {
	inner    transport.Conn
	recorder *RecordingWebSocketDialer
}

var _ transport.Conn = (*recordingWebSocketConn)(nil)

func (c *recordingWebSocketConn) ReadMessage() (int, []byte, error) {
	messageType, payload, err := c.inner.ReadMessage()
	if err == nil {
		sequence := c.recorder.recordMessage(DirectionServerToClient, payload)
		c.recorder.commitMessage(sequence)
	}
	return messageType, payload, err
}

func (c *recordingWebSocketConn) WriteMessage(messageType int, payload []byte) error {
	// Reserve the outbound event before invoking the wrapped connection. A
	// hermetic provider may synchronously enqueue a response while processing
	// this write; recording after the call lets that response appear before
	// its causal client event in the capture.
	sequence := c.recorder.recordMessage(DirectionClientToServer, payload)
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		c.recorder.discardMessage(sequence)
		return err
	}
	c.recorder.commitMessage(sequence)
	return nil
}

func (c *recordingWebSocketConn) Close() error {
	return c.inner.Close()
}

func newCaptureTemporary(path string) (*os.File, error) {
	directory := filepath.Dir(path)
	base := filepath.Base(path)
	temporary, err := os.CreateTemp(directory, "."+base+".tmp-")
	if err != nil {
		return nil, fmt.Errorf("create session capture temporary file: %w", err)
	}
	if err := temporary.Chmod(protectedCaptureFileMode); err != nil {
		return nil, errors.Join(fmt.Errorf("protect session capture temporary file: %w", err), temporary.Close(), os.Remove(temporary.Name()))
	}
	return temporary, nil
}

func writeCaptureCoverageHeader(writer io.Writer, capture SessionCapture) error {
	if _, err := io.WriteString(writer, `{"version":`); err != nil {
		return fmt.Errorf("write session capture version field: %w", err)
	}
	if err := writeCaptureJSONValue(writer, capture.Version); err != nil {
		return err
	}
	if err := writeCaptureJSONField(writer, ",\"provider\":", capture.Provider); err != nil {
		return err
	}
	if err := writeCaptureJSONField(writer, ",\"session\":", capture.Session); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, `,"records":[`); err != nil {
		return fmt.Errorf("write session capture records field: %w", err)
	}
	return nil
}
