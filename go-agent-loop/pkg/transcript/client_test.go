package transcript

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"
)

func TestClientCaptureRecordsOrderedDeviceAndWebSocketBoundaries(t *testing.T) {
	base := time.Date(2026, time.August, 16, 19, 0, 0, 0, time.UTC)
	metadataCalls := 0
	metadata := func() (uint64, time.Time) {
		metadataCalls++
		return uint64(metadataCalls), base.Add(time.Duration(metadataCalls) * time.Millisecond)
	}
	sink := &clientRecordSink{}
	capture := NewClientCapture(sink, metadata)

	input := &scriptedReader{chunks: [][]byte{
		{0x10, 0x00, 0xff, 0x01},
		{0x20, 0x00, 0xfe, 0x02},
	}}
	deviceInput := capture.WrapDeviceInput(input)
	for index := 0; index < 2; index++ {
		buffer := make([]byte, 4)
		n, err := deviceInput.Read(buffer)
		if err != nil || n != len(buffer) {
			t.Fatalf("device input %d = (%d, %v), want four bytes and nil", index, n, err)
		}
	}

	transport := &scriptedWebSocket{
		incoming: [][]byte{{0x31, 0x00, 0xfd}, {0x32, 0x00, 0xfc}},
	}
	webSocket := capture.WrapWebSocket(transport)
	outgoing := [][]byte{
		{0x41, 0x00, 0xfb},
		{0x42, 0x00, 0xfa},
		{0x43, 0x00, 0xf9},
	}
	for index, payload := range outgoing {
		if err := webSocket.WriteMessage(1+index, payload); err != nil {
			t.Fatalf("websocket send %d: %v", index, err)
		}
	}
	for index, wantPayload := range transport.incoming {
		messageType, payload, err := webSocket.ReadMessage()
		if err != nil || messageType != 7+index || !bytes.Equal(payload, wantPayload) {
			t.Fatalf("websocket receive %d = (%d, %x, %v), want (%d, %x, nil)",
				index, messageType, payload, err, 7+index, wantPayload)
		}
	}

	output := &bytes.Buffer{}
	deviceOutput := capture.WrapDeviceOutput(output)
	played := [][]byte{{0x51, 0x00, 0xf8, 0x03}, {0x52, 0x00, 0xf7, 0x04}}
	for index, payload := range played {
		n, err := deviceOutput.Write(payload)
		if err != nil || n != len(payload) {
			t.Fatalf("device output %d = (%d, %v), want full write and nil", index, n, err)
		}
	}

	want := []Record{
		NewRecord(1, base.Add(1*time.Millisecond), PeerClient, DirectionIn, StreamDeviceIn, []byte{0x10, 0x00, 0xff, 0x01}),
		NewRecord(2, base.Add(2*time.Millisecond), PeerClient, DirectionIn, StreamDeviceIn, []byte{0x20, 0x00, 0xfe, 0x02}),
		NewRecord(3, base.Add(3*time.Millisecond), PeerClient, DirectionOut, StreamWS, []byte{0x41, 0x00, 0xfb}),
		NewRecord(4, base.Add(4*time.Millisecond), PeerClient, DirectionOut, StreamWS, []byte{0x42, 0x00, 0xfa}),
		NewRecord(5, base.Add(5*time.Millisecond), PeerClient, DirectionOut, StreamWS, []byte{0x43, 0x00, 0xf9}),
		NewRecord(6, base.Add(6*time.Millisecond), PeerClient, DirectionIn, StreamWS, []byte{0x31, 0x00, 0xfd}),
		NewRecord(7, base.Add(7*time.Millisecond), PeerClient, DirectionIn, StreamWS, []byte{0x32, 0x00, 0xfc}),
		NewRecord(8, base.Add(8*time.Millisecond), PeerClient, DirectionOut, StreamDeviceOut, []byte{0x51, 0x00, 0xf8, 0x03}),
		NewRecord(9, base.Add(9*time.Millisecond), PeerClient, DirectionOut, StreamDeviceOut, []byte{0x52, 0x00, 0xf7, 0x04}),
	}
	if metadataCalls != len(want) {
		t.Fatalf("metadata calls = %d, want %d", metadataCalls, len(want))
	}
	if len(sink.records) != len(want) {
		t.Fatalf("captured records = %d, want exactly %d", len(sink.records), len(want))
	}
	for index := range want {
		if !recordsEqual(sink.records[index], want[index]) {
			t.Fatalf("record %d = %+v, want %+v", index, sink.records[index], want[index])
		}
	}

	wantCounts := map[string]int{
		string(DirectionIn) + "/" + string(StreamDeviceIn):   2,
		string(DirectionOut) + "/" + string(StreamWS):        3,
		string(DirectionIn) + "/" + string(StreamWS):         2,
		string(DirectionOut) + "/" + string(StreamDeviceOut): 2,
	}
	gotCounts := make(map[string]int)
	for _, record := range sink.records {
		gotCounts[string(record.Direction)+"/"+string(record.Stream)]++
	}
	if !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Fatalf("stream counts = %v, want %v", gotCounts, wantCounts)
	}

	seen := make(map[string]int, len(sink.records))
	for _, record := range sink.records {
		seen[string(record.Payload)]++
	}
	for _, record := range want {
		if seen[string(record.Payload)] != 1 {
			t.Fatalf("payload %x appeared %d times, want exactly once", record.Payload, seen[string(record.Payload)])
		}
	}
	if !bytes.Equal(output.Bytes(), bytes.Join(played, nil)) {
		t.Fatalf("device output bytes = %x, want %x", output.Bytes(), bytes.Join(played, nil))
	}
}

func TestClientCaptureLeavesLiveResultsUnchangedWhenTranscriptDegrades(t *testing.T) {
	liveReadErr := errors.New("input read failed")
	liveWriteErr := errors.New("output write failed")
	liveSendErr := errors.New("websocket send failed")
	liveReceiveErr := errors.New("websocket receive failed")
	recordingErr := errors.New("transcript unavailable")
	reports := make([]error, 0, 1)
	capture := NewClientCapture(&errorClientRecordSink{err: recordingErr}, func() (uint64, time.Time) {
		return 17, time.Unix(17, 0)
	}, WithClientReporter(func(err error) {
		reports = append(reports, err)
	}))

	readPayload := []byte{0x01, 0xff, 0x00}
	baselineReader := &scriptedReader{chunks: [][]byte{readPayload}, err: liveReadErr}
	capturedReader := &scriptedReader{chunks: [][]byte{readPayload}, err: liveReadErr}
	baselineBuffer := make([]byte, len(readPayload))
	capturedBuffer := make([]byte, len(readPayload))
	baselineN, baselineErr := baselineReader.Read(baselineBuffer)
	capturedN, capturedErr := capture.WrapDeviceInput(capturedReader).Read(capturedBuffer)
	if capturedN != baselineN || !errors.Is(capturedErr, liveReadErr) || capturedErr != baselineErr ||
		!bytes.Equal(capturedBuffer, baselineBuffer) || capturedReader.calls != baselineReader.calls {
		t.Fatalf("device input changed: captured=(%d,%v,%x,%d), baseline=(%d,%v,%x,%d)",
			capturedN, capturedErr, capturedBuffer, capturedReader.calls,
			baselineN, baselineErr, baselineBuffer, baselineReader.calls)
	}

	baselineWriter := &scriptedWriter{n: 2, err: liveWriteErr}
	capturedWriter := &scriptedWriter{n: 2, err: liveWriteErr}
	writePayload := []byte{0x02, 0xfe, 0x03, 0xfd}
	baselineN, baselineErr = baselineWriter.Write(writePayload)
	capturedN, capturedErr = capture.WrapDeviceOutput(capturedWriter).Write(writePayload)
	if capturedN != baselineN || capturedErr != baselineErr || !errors.Is(capturedErr, liveWriteErr) ||
		!bytes.Equal(capturedWriter.seen, baselineWriter.seen) || capturedWriter.calls != baselineWriter.calls {
		t.Fatalf("device output changed: captured=(%d,%v,%x,%d), baseline=(%d,%v,%x,%d)",
			capturedN, capturedErr, capturedWriter.seen, capturedWriter.calls,
			baselineN, baselineErr, baselineWriter.seen, baselineWriter.calls)
	}

	baselineTransport := &scriptedWebSocket{sendErr: liveSendErr, receiveErr: liveReceiveErr}
	capturedTransport := &scriptedWebSocket{sendErr: liveSendErr, receiveErr: liveReceiveErr}
	baselineConnection := baselineTransport
	capturedConnection := capture.WrapWebSocket(capturedTransport)
	sendPayload := []byte{0x04, 0xfc}
	if capturedErr = capturedConnection.WriteMessage(9, sendPayload); capturedErr != baselineConnection.WriteMessage(9, sendPayload) ||
		!errors.Is(capturedErr, liveSendErr) || capturedTransport.sendCalls != baselineTransport.sendCalls {
		t.Fatalf("websocket send changed: captured=(%v,%d), baseline=(%v,%d)", capturedErr, capturedTransport.sendCalls, baselineSendErr(baselineTransport), baselineTransport.sendCalls)
	}
	gotType, gotPayload, capturedErr := capturedConnection.ReadMessage()
	wantType, wantPayload, baselineErr := baselineConnection.ReadMessage()
	if gotType != wantType || !bytes.Equal(gotPayload, wantPayload) || capturedErr != baselineErr || !errors.Is(capturedErr, liveReceiveErr) ||
		capturedTransport.receiveCalls != baselineTransport.receiveCalls {
		t.Fatalf("websocket receive changed: captured=(%d,%x,%v,%d), baseline=(%d,%x,%v,%d)",
			gotType, gotPayload, capturedErr, capturedTransport.receiveCalls,
			wantType, wantPayload, baselineErr, baselineTransport.receiveCalls)
	}
	if len(reports) != 1 || !errors.Is(reports[0], recordingErr) {
		t.Fatalf("transcript reports = %v, want one report retaining sink error", reports)
	}
}

func TestClientCaptureCopiesOutboundPayloadBeforeLiveMutation(t *testing.T) {
	sink := &clientRecordSink{}
	capture := NewClientCapture(sink, func() (uint64, time.Time) {
		return 1, time.Unix(1, 0)
	})

	devicePayload := []byte{0x10, 0x00, 0xff}
	deviceWriter := capture.WrapDeviceOutput(mutatingClientWriter{})
	if n, err := deviceWriter.Write(devicePayload); n != len(devicePayload) || err != nil {
		t.Fatalf("device Write = (%d, %v), want full success", n, err)
	}

	webSocketPayload := []byte{0x20, 0x00, 0xfe}
	webSocket := capture.WrapWebSocket(mutatingWebSocket{})
	if err := webSocket.WriteMessage(1, webSocketPayload); err != nil {
		t.Fatalf("websocket WriteMessage: %v", err)
	}

	if len(sink.records) != 2 {
		t.Fatalf("captured records = %d, want 2", len(sink.records))
	}
	if !bytes.Equal(sink.records[0].Payload, []byte{0x10, 0x00, 0xff}) {
		t.Fatalf("device payload = %x, want original bytes", sink.records[0].Payload)
	}
	if !bytes.Equal(sink.records[1].Payload, []byte{0x20, 0x00, 0xfe}) {
		t.Fatalf("websocket payload = %x, want original bytes", sink.records[1].Payload)
	}
}

type clientRecordSink struct {
	records []Record
}

func (s *clientRecordSink) Write(record Record) error {
	s.records = append(s.records, cloneRecord(record))
	return nil
}

type errorClientRecordSink struct{ err error }

func (s *errorClientRecordSink) Write(Record) error { return s.err }

type scriptedReader struct {
	chunks [][]byte
	err    error
	calls  int
}

func (r *scriptedReader) Read(destination []byte) (int, error) {
	r.calls++
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	r.chunks = r.chunks[1:]
	n := copy(destination, chunk)
	if r.err != nil {
		return n, r.err
	}
	return n, nil
}

type scriptedWriter struct {
	n     int
	err   error
	seen  []byte
	calls int
}

type mutatingClientWriter struct{}

func (mutatingClientWriter) Write(source []byte) (int, error) {
	for index := range source {
		source[index] = 0
	}
	return len(source), nil
}

func (w *scriptedWriter) Write(source []byte) (int, error) {
	w.calls++
	w.seen = append(w.seen[:0], source...)
	return w.n, w.err
}

type scriptedWebSocket struct {
	incoming     [][]byte
	sendErr      error
	receiveErr   error
	sendCalls    int
	receiveCalls int
}

func (c *scriptedWebSocket) WriteMessage(_ int, _ []byte) error {
	c.sendCalls++
	return c.sendErr
}

func (c *scriptedWebSocket) ReadMessage() (int, []byte, error) {
	c.receiveCalls++
	if c.receiveErr != nil {
		return 0, nil, c.receiveErr
	}
	if len(c.incoming) == 0 {
		return 0, nil, io.EOF
	}
	payload := c.incoming[0]
	c.incoming = c.incoming[1:]
	return 6 + c.receiveCalls, payload, nil
}

type mutatingWebSocket struct{}

func (mutatingWebSocket) WriteMessage(_ int, payload []byte) error {
	for index := range payload {
		payload[index] = 0
	}
	return nil
}

func (mutatingWebSocket) ReadMessage() (int, []byte, error) {
	return 0, nil, io.EOF
}

func baselineSendErr(c *scriptedWebSocket) error {
	return c.sendErr
}
