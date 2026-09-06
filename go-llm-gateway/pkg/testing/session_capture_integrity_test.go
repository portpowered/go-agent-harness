package testing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionCaptureIntegritySealAndLoad(t *testing.T) {
	capture := protectedTestCapture()
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "valid.session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	loaded, err := LoadSessionCapture(path)
	if err != nil {
		t.Fatalf("LoadSessionCapture: %v", err)
	}
	if loaded.Version != SessionCaptureVersion {
		t.Fatalf("version = %d, want %d", loaded.Version, SessionCaptureVersion)
	}
	if loaded.Integrity.Algorithm != SessionCaptureIntegrityAlgorithm {
		t.Fatalf("algorithm = %q, want %q", loaded.Integrity.Algorithm, SessionCaptureIntegrityAlgorithm)
	}
	if loaded.Integrity.Coverage != SessionCaptureIntegrityCoverage {
		t.Fatalf("coverage = %q, want %q", loaded.Integrity.Coverage, SessionCaptureIntegrityCoverage)
	}
	digest, err := ComputeSessionCaptureDigest(loaded)
	if err != nil {
		t.Fatalf("ComputeSessionCaptureDigest: %v", err)
	}
	if loaded.Integrity.Digest != digest {
		t.Fatalf("digest = %q, recomputed %q", loaded.Integrity.Digest, digest)
	}
	if loaded.Integrity.Digest != strings.ToLower(loaded.Integrity.Digest) || len(loaded.Integrity.Digest) != 64 {
		t.Fatalf("digest is not lowercase SHA-256 hex: %q", loaded.Integrity.Digest)
	}
}

func TestSessionCaptureIntegrityDetectsReplayRelevantPayloadMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.session.json")
	data := marshalProtectedTestCapture(t)
	mutated := []byte(strings.Replace(string(data), "captured text", "corrupted text", 1))
	if string(mutated) == string(data) {
		t.Fatal("test mutation did not change capture bytes")
	}
	if err := os.WriteFile(path, mutated, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	_, err := LoadSessionCapture(path)
	if err == nil {
		t.Fatal("LoadSessionCapture accepted a corrupted capture")
	}
	var validationErr *SessionCaptureValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want SessionCaptureValidationError", err)
	}
	if validationErr.Classification != SessionCaptureErrorClassIntegrityChecksum {
		t.Fatalf("classification = %q, want %q", validationErr.Classification, SessionCaptureErrorClassIntegrityChecksum)
	}
	if validationErr.Path != path || validationErr.Algorithm != SessionCaptureIntegrityAlgorithm {
		t.Fatalf("validation error = %+v, want path and algorithm", validationErr)
	}
	if validationErr.Expected == "" || validationErr.Actual == "" || validationErr.Expected == validationErr.Actual {
		t.Fatalf("validation error lacks differing bounded digest details: %+v", validationErr)
	}
	if len(validationErr.Expected) > 80 || len(validationErr.Actual) > 80 {
		t.Fatalf("digest details are not bounded: %+v", validationErr)
	}
	if !errors.Is(err, ErrSessionCaptureIntegrity) {
		t.Fatalf("error = %v, want errors.Is(ErrSessionCaptureIntegrity)", err)
	}
}

func TestSessionCaptureIntegrityReportsFirstStructuralDifference(t *testing.T) {
	capture := protectedTestCapture()
	capture.Records[0].Sequence = 2
	capture.Records[1].Sequence = 2
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "structural.session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	_, err = LoadSessionCapture(path)
	if err == nil {
		t.Fatal("LoadSessionCapture accepted invalid event ordering")
	}
	var validationErr *SessionCaptureValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want SessionCaptureValidationError", err)
	}
	if validationErr.Classification != SessionCaptureErrorClassStructure {
		t.Fatalf("classification = %q, want %q", validationErr.Classification, SessionCaptureErrorClassStructure)
	}
	if validationErr.FieldPath != "/records/1/sequence" || validationErr.RecordSequence != 2 {
		t.Fatalf("validation error = %+v, want first record sequence pointer", validationErr)
	}
	if !errors.Is(err, ErrSessionCaptureStructure) {
		t.Fatalf("error = %v, want errors.Is(ErrSessionCaptureStructure)", err)
	}
}

func TestSessionCaptureIntegrityRejectsUnprotectedInputs(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{name: "legacy envelope", data: `{"version":1,"provider":{},"session":{},"records":[]}`},
		{name: "legacy event array", data: `[{"sequence":1}]`},
		{name: "missing integrity", data: `{"version":2,"provider":{},"session":{},"records":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unprotected.session.json")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatalf("write capture: %v", err)
			}
			_, err := LoadSessionCapture(path)
			if err == nil {
				t.Fatal("LoadSessionCapture accepted unprotected input")
			}
			if !errors.Is(err, ErrSessionCaptureIntegrityUnavailable) && tc.name != "missing integrity" {
				t.Fatalf("error = %v, want integrity-unavailable classification", err)
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "integrity") {
				t.Fatalf("error = %v, want path and integrity classification", err)
			}
		})
	}
}

func TestSessionCaptureReplayLoaderAcceptsLegacyV1WithReducedGuarantee(t *testing.T) {
	capture := protectedTestCapture()
	capture.Version = SessionCaptureLegacyVersion
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy capture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "legacy.session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write legacy capture: %v", err)
	}

	loaded, err := LoadSessionCaptureForReplay(path)
	if err != nil {
		t.Fatalf("LoadSessionCaptureForReplay: %v", err)
	}
	if loaded.IntegrityVerified {
		t.Fatal("legacy capture was reported as integrity-verified")
	}
	if loaded.Capture.Version != SessionCaptureLegacyVersion || len(loaded.Capture.Records) != len(capture.Records) {
		t.Fatalf("loaded legacy capture = %+v, want version %d and %d records", loaded.Capture, SessionCaptureLegacyVersion, len(capture.Records))
	}
	warning := loaded.IntegrityWarning(path)
	if !strings.Contains(warning, path) || !strings.Contains(warning, "integrity was unavailable") || !strings.Contains(warning, "reduced guarantees") {
		t.Fatalf("legacy warning = %q, want path and reduced-guarantee details", warning)
	}
}

func TestSessionCaptureIntegrityRejectsTruncatedJSON(t *testing.T) {
	data := marshalProtectedTestCapture(t)
	if len(data) < 2 {
		t.Fatal("protected capture unexpectedly empty")
	}
	path := filepath.Join(t.TempDir(), "truncated.session.json")
	if err := os.WriteFile(path, data[:len(data)-1], 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	_, err := LoadSessionCapture(path)
	if err == nil {
		t.Fatal("LoadSessionCapture accepted truncated JSON")
	}
	var validationErr *SessionCaptureValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want SessionCaptureValidationError", err)
	}
	if validationErr.Classification != SessionCaptureErrorClassStructure || validationErr.FieldPath != "$" {
		t.Fatalf("validation error = %+v, want structural root error", validationErr)
	}
	if !errors.Is(err, ErrSessionCaptureStructure) {
		t.Fatalf("error = %v, want errors.Is(ErrSessionCaptureStructure)", err)
	}
}

func TestSessionCaptureIntegrityRejectsMalformedMetadata(t *testing.T) {
	data := marshalProtectedTestCapture(t)
	mutated := []byte(strings.Replace(string(data), `"algorithm": "sha256"`, `"algorithm": 7`, 1))
	if string(mutated) == string(data) {
		t.Fatal("test mutation did not change integrity metadata")
	}
	path := filepath.Join(t.TempDir(), "malformed-integrity.session.json")
	if err := os.WriteFile(path, mutated, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	_, err := LoadSessionCapture(path)
	if err == nil {
		t.Fatal("LoadSessionCapture accepted malformed integrity metadata")
	}
	var validationErr *SessionCaptureValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want SessionCaptureValidationError", err)
	}
	if validationErr.Classification != SessionCaptureErrorClassIntegrityMetadata || validationErr.FieldPath != "/integrity/algorithm" {
		t.Fatalf("validation error = %+v, want metadata algorithm error", validationErr)
	}
	if !errors.Is(err, ErrSessionCaptureIntegrity) {
		t.Fatalf("error = %v, want errors.Is(ErrSessionCaptureIntegrity)", err)
	}
}

func protectedTestCapture() SessionCapture {
	return SessionCapture{
		Version:  SessionCaptureVersion,
		Provider: SessionProviderMetadata{Name: "test", Model: "test-model"},
		Session: SessionMetadata{
			ID:                "sess-integrity-test",
			StartedAtUTC:      time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
			FixtureProvenance: SessionFixtureProvenanceSynthetic,
		},
		Records: []CapturedSessionEvent{
			{
				Sequence:    1,
				Direction:   DirectionServerToClient,
				TimestampMs: 0,
				Type:        string(messages.StreamTypeTextDelta),
				PayloadType: SessionPayloadTypeStreamMessage,
				Payload:     json.RawMessage(`{"type":"TEXT.DELTA","value":{"type":"delta_text","content":"captured text"}}`),
			},
			{
				Sequence:    2,
				Direction:   DirectionClientToServer,
				TimestampMs: 1,
				Type:        string(messages.StreamTypeResponseCreate),
				PayloadType: SessionPayloadTypeStreamMessage,
				Payload:     json.RawMessage(`{"type":"RESPONSE.CREATE","value":{"type":"response_create"}}`),
			},
		},
	}
}

func marshalProtectedTestCapture(t *testing.T) []byte {
	t.Helper()
	data, err := json.MarshalIndent(protectedTestCapture(), "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	return data
}

func TestWriteSessionCaptureFromReaderMatchesCanonicalDigest(t *testing.T) {
	records := []CapturedSessionEvent{
		{
			Sequence: 1, Direction: DirectionClientToServer, TimestampMs: 0,
			Type: "session.update", PayloadType: SessionPayloadTypeWebSocketMessage,
			Payload: json.RawMessage(`{"type":"session.update","nested":{"quote":"a\\n\\\"b"}}`),
		},
		{
			Sequence: 2, Direction: DirectionClientToServer, TimestampMs: 15,
			Type: "response.create", PayloadType: SessionPayloadTypeWebSocketMessage,
			Payload: json.RawMessage(`{"type":"response.create","items":[1,true,null]}`),
		},
	}
	capture := SessionCapture{
		Version:            SessionCaptureVersion,
		Provider:           SessionProviderMetadata{Name: "grok", Model: "grok-realtime"},
		Session:            SessionMetadata{ID: "session-1", StartedAtUTC: "2026-09-05T12:00:00Z"},
		Records:            records,
		EndsWithDisconnect: true,
	}
	canonical, err := SealSessionCapture(capture)
	if err != nil {
		t.Fatalf("SealSessionCapture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "capture.json")
	reader := &sliceCaptureReader{records: append([]CapturedSessionEvent(nil), records...)}
	if err := WriteSessionCaptureFromReader(path, capture, reader); err != nil {
		t.Fatalf("WriteSessionCaptureFromReader: %v", err)
	}
	loaded, err := LoadSessionCapture(path)
	if err != nil {
		t.Fatalf("LoadSessionCapture: %v", err)
	}
	if loaded.Integrity != canonical.Integrity {
		t.Fatalf("integrity = %#v, want canonical %#v", loaded.Integrity, canonical.Integrity)
	}
	if !reflect.DeepEqual(loaded.Records, records) {
		t.Fatalf("records = %#v, want %#v", loaded.Records, records)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat capture: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("capture mode = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteSessionCaptureFromReaderSupportsEmptyCaptureVariants(t *testing.T) {
	for _, endsWithDisconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "disconnect"}[endsWithDisconnect], func(t *testing.T) {
			capture := SessionCapture{
				Version:            SessionCaptureVersion,
				Provider:           SessionProviderMetadata{Name: "provider", Model: "model"},
				Session:            SessionMetadata{StartedAtUTC: "2026-09-05T12:00:00Z"},
				EndsWithDisconnect: endsWithDisconnect,
			}
			sealed, err := SealSessionCapture(capture)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "empty.json")
			if err := WriteSessionCaptureFromReader(path, capture, &sliceCaptureReader{}); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadSessionCapture(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Integrity != sealed.Integrity || len(loaded.Records) != 0 {
				t.Fatalf("loaded = %#v, want empty capture with integrity %#v", loaded, sealed.Integrity)
			}
		})
	}
}

func TestWriteSessionCaptureFromReaderUsesCanonicalRecordValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.json")
	reader := &sliceCaptureReader{records: []CapturedSessionEvent{{
		Sequence: 1, Direction: DirectionClientToServer, Type: " ",
		PayloadType: SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"invalid"}`),
	}}}
	err := WriteSessionCaptureFromReader(path, SessionCapture{Version: SessionCaptureVersion}, reader)
	if !errors.Is(err, ErrSessionCaptureStructure) {
		t.Fatalf("error = %v, want structure error", err)
	}
	var validationErr *SessionCaptureValidationError
	if !errors.As(err, &validationErr) || validationErr.FieldPath != "/records/0/type" {
		t.Fatalf("validation error = %#v, want canonical type path", validationErr)
	}
}

func TestWriteSessionCaptureFromReaderRejectsEmptyPayloadType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.json")
	err := WriteSessionCaptureFromReader(path, SessionCapture{Version: SessionCaptureVersion}, &sliceCaptureReader{records: []CapturedSessionEvent{{
		Sequence:  1,
		Direction: DirectionClientToServer,
		Type:      "session.update",
		Payload:   json.RawMessage(`{"type":"session.update"}`),
	}}})
	if !errors.Is(err, ErrSessionCaptureStructure) {
		t.Fatalf("error = %v, want structure error", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("capture path stat error = %v, want unpublished path", statErr)
	}
}

type sliceCaptureReader struct {
	records []CapturedSessionEvent
	index   int
}

func (r *sliceCaptureReader) Next() (CapturedSessionEvent, bool, error) {
	if r.index == len(r.records) {
		return CapturedSessionEvent{}, false, nil
	}
	record := r.records[r.index]
	r.index++
	return record, true, nil
}

var _ SessionCaptureRecordReader = (*sliceCaptureReader)(nil)

func TestStreamingRecordingDialerSettlesProviderReservations(t *testing.T) {
	sink := &recordingSink{}
	live := &testWebSocketDialer{conn: &testWebSocketConn{inbound: [][]byte{[]byte(`{"type":"session.created"}`)}}}
	dialer, err := NewRecordingWebSocketDialerWithSink(live, "provider", "model", sink)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("wss://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"session.update"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sink.commits, []int{1, 2}) {
		t.Fatalf("commits = %v, want [1 2]", sink.commits)
	}
	if len(sink.discards) != 0 || len(sink.events) != 2 {
		t.Fatalf("sink events = %d discards = %v, want two events and no discards", len(sink.events), sink.discards)
	}
}

func TestStreamingRecordingDialerDiscardsFailedOutboundReservation(t *testing.T) {
	sink := &recordingSink{}
	dialer, err := NewRecordingWebSocketDialerWithSink(&testWebSocketDialer{conn: failingWriteConn{}}, "provider", "model", sink)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("wss://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := conn.WriteMessage(1, []byte(`{"type":"session.update"}`))
	if !errors.Is(writeErr, errFailedWrite) {
		t.Fatalf("WriteMessage error = %v, want failed write", writeErr)
	}
	if !reflect.DeepEqual(sink.commits, []int(nil)) || !reflect.DeepEqual(sink.discards, []int{1}) {
		t.Fatalf("commits = %v discards = %v, want no commits and discard [1]", sink.commits, sink.discards)
	}
}

func TestStreamingRecordingDialerDoesNotPublishAfterAdmissionFailure(t *testing.T) {
	admissionErr := errors.New("capture admission failed")
	sink := &recordingSink{appendErr: admissionErr}
	dialer, err := NewRecordingWebSocketDialerWithSink(&testWebSocketDialer{conn: &testWebSocketConn{}}, "provider", "model", sink)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("wss://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"session.update"}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	if err := dialer.FlushToFile(filepath.Join(t.TempDir(), "capture.json")); !errors.Is(err, admissionErr) {
		t.Fatalf("flush error = %v, want admission failure", err)
	}
	if sink.flushCalled {
		t.Fatal("failed sink was asked to publish a partial capture")
	}
	if !sink.abortCalled {
		t.Fatal("failed sink was not aborted")
	}
	if sink.appendCalls != 1 {
		t.Fatalf("sink append calls = %d, want one after the latched admission failure", sink.appendCalls)
	}
}

func TestStreamingRecordingDialerRejectsNilSink(t *testing.T) {
	if _, err := NewRecordingWebSocketDialerWithSink(nil, "provider", "model", nil); err == nil {
		t.Fatal("nil sink was accepted")
	}
}

type recordingSink struct {
	events      []CapturedSessionEvent
	commits     []int
	discards    []int
	appendErr   error
	appendCalls int
	flushCalled bool
	abortCalled bool
}

func (s *recordingSink) Append(event CapturedSessionEvent) error {
	s.appendCalls++
	if s.appendErr != nil {
		return s.appendErr
	}
	s.events = append(s.events, cloneCapturedEvent(event))
	return nil
}

func (s *recordingSink) Commit(sequence int) error {
	s.commits = append(s.commits, sequence)
	return nil
}

func (s *recordingSink) Discard(sequence int) error {
	s.discards = append(s.discards, sequence)
	return nil
}

func (s *recordingSink) FlushToFile(string, SessionCapture) error {
	s.flushCalled = true
	return nil
}

func (s *recordingSink) Abort() error {
	s.abortCalled = true
	return nil
}

type failingWriteConn struct{}

func (failingWriteConn) ReadMessage() (int, []byte, error) { return 0, nil, os.ErrClosed }
func (failingWriteConn) WriteMessage(int, []byte) error    { return errFailedWrite }
func (failingWriteConn) Close() error                      { return nil }

type captureTestError string

func (e captureTestError) Error() string { return string(e) }

const errFailedWrite captureTestError = "write failed"
