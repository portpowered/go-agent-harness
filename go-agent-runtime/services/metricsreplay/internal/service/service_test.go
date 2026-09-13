package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
)

type testClock struct{}

func (testClock) Now() time.Time { return time.Unix(123, 0).UTC() }

type testLoader struct {
	fixture metricsreplay.Fixture
	err     error
	calls   int
}

func (l *testLoader) Load(context.Context, string) (metricsreplay.Fixture, error) {
	l.calls++
	return l.fixture, l.err
}

type testRunner struct {
	run   func(context.Context, metricsreplay.RunRequest) error
	calls int
}

func (r *testRunner) Run(ctx context.Context, request metricsreplay.RunRequest) error {
	r.calls++
	if r.run == nil {
		return nil
	}
	return r.run(ctx, request)
}

type testSink struct {
	totals       map[string]int64
	series       []metricsreplay.SnapshotSeries
	snapshotErr  error
	closeErr     error
	closeCalls   int
	records      []metricsreplay.Series
	snapshotCall int
}

func newTestSink() *testSink {
	return &testSink{totals: make(map[string]int64)}
}

func (s *testSink) Record(direction metricsreplay.Direction, modality metricsreplay.Modality, bytes int64) error {
	if bytes < 0 {
		return errors.New("negative test metric")
	}
	key := string(direction) + "/" + string(modality)
	s.totals[key] += bytes
	s.records = append(s.records, metricsreplay.Series{Direction: direction, Modality: modality, ReportedTotal: bytes})
	return nil
}

func (s *testSink) Snapshot() (metricsreplay.Snapshot, error) {
	s.snapshotCall++
	if s.snapshotErr != nil {
		return metricsreplay.Snapshot{}, s.snapshotErr
	}
	if s.series != nil {
		return metricsreplay.Snapshot{Series: append([]metricsreplay.SnapshotSeries(nil), s.series...)}, nil
	}
	series := make([]metricsreplay.SnapshotSeries, 0, len(s.totals))
	for key, total := range s.totals {
		parts := strings.SplitN(key, "/", 2)
		series = append(series, metricsreplay.SnapshotSeries{
			Direction:  metricsreplay.Direction(parts[0]),
			Modality:   metricsreplay.Modality(parts[1]),
			TotalBytes: total,
		})
	}
	return metricsreplay.Snapshot{Series: series}, nil
}

func (s *testSink) Close() error {
	s.closeCalls++
	return s.closeErr
}

type testSinkFactory struct {
	sink  *testSink
	err   error
	calls int
}

func (f *testSinkFactory) New() (metricsreplay.Sink, error) {
	f.calls++
	return f.sink, f.err
}

func testService(fixture metricsreplay.Fixture, loaderErr error, runner *testRunner, sinkFactory *testSinkFactory) (*Service, *testLoader) {
	loader := &testLoader{fixture: fixture, err: loaderErr}
	return New(Dependencies{
		Clock:   testClock{},
		Runner:  runner,
		Loader:  loader,
		NewSink: sinkFactory.New,
	}), loader
}

func rawRecord(sequence int, direction metricsreplay.WireDirection, typeName, payload string) metricsreplay.Record {
	return metricsreplay.Record{
		Sequence:    sequence,
		Direction:   direction,
		Type:        typeName,
		PayloadType: metricsreplay.PayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(payload),
	}
}

func streamRecord(sequence int, direction metricsreplay.WireDirection, typeName, payload string) metricsreplay.Record {
	record := rawRecord(sequence, direction, typeName, payload)
	record.PayloadType = metricsreplay.PayloadTypeStreamMessage
	return record
}

func TestCollectReconcilesRawAndStreamWireWithStableSeries(t *testing.T) {
	fixture := metricsreplay.Fixture{Records: []metricsreplay.Record{
		rawRecord(1, metricsreplay.WireDirectionClientToServer, "conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`),
		rawRecord(2, metricsreplay.WireDirectionClientToServer, "input_audio_buffer.append", `{"type":"input_audio_buffer.append","audio":"AQIDBA=="}`),
		rawRecord(3, metricsreplay.WireDirectionServerToClient, "response.audio.delta", `{"type":"response.audio.delta","delta":"AQID"}`),
		rawRecord(4, metricsreplay.WireDirectionServerToClient, "response.text.delta", `{"type":"response.text.delta","delta":"hello"}`),
		rawRecord(5, metricsreplay.WireDirectionServerToClient, "conversation.item.input_audio_transcription.delta", `{"type":"conversation.item.input_audio_transcription.delta","delta":"heard"}`),
		rawRecord(6, metricsreplay.WireDirectionServerToClient, "response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"raw-call","delta":"{\"x\":"}`),
		rawRecord(7, metricsreplay.WireDirectionServerToClient, "response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"raw-call","delta":"1}"}`),
		rawRecord(8, metricsreplay.WireDirectionServerToClient, "response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"raw-call","arguments":"{\"x\":1}"}`),
		streamRecord(9, metricsreplay.WireDirectionServerToClient, "TEXT.DELTA", `{"type":"TEXT.DELTA","role":"assistant","value":{"type":"delta_text","content":"world"}}`),
		streamRecord(10, metricsreplay.WireDirectionServerToClient, "AUDIO.DELTA", `{"type":"AUDIO.DELTA","role":"assistant","value":{"type":"delta_audio","content":"AQI="}}`),
		streamRecord(11, metricsreplay.WireDirectionServerToClient, "TOOLCALL.DELTA", `{"type":"TOOLCALL.DELTA","tool_call_id":"stream-call","value":{"type":"input_json_delta","partial_json":"{}"}}`),
		streamRecord(12, metricsreplay.WireDirectionServerToClient, "TOOLCALL.END", `{"type":"TOOLCALL.END","tool_call_id":"stream-call","value":{"type":"tool_use_end","tool_call_id":"stream-call","arguments":"{}"}}`),
		streamRecord(13, metricsreplay.WireDirectionServerToClient, "TRANSCRIPT.DELTA", `{"type":"TRANSCRIPT.DELTA","role":"user","value":{"type":"transcript_delta","text":"again"}}`),
	}}

	sink := newTestSink()
	sink.series = []metricsreplay.SnapshotSeries{
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityTool, TotalBytes: 9},
		{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityText, TotalBytes: 15},
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityAudio, TotalBytes: 5},
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityText, TotalBytes: 10},
		{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityAudio, TotalBytes: 4},
	}
	factory := &testSinkFactory{sink: sink}
	clock := testClock{}
	runner := &testRunner{run: func(_ context.Context, request metricsreplay.RunRequest) error {
		if request.Fixture != "fixture.json" || request.Prompt != "prompt" || request.Clock != clock || request.Recorder == nil {
			t.Fatalf("runner request = %#v, want explicit fixture, prompt, clock and recorder", request)
		}
		for _, observation := range []struct {
			direction metricsreplay.Direction
			modality  metricsreplay.Modality
			bytes     int64
		}{
			{metricsreplay.DirectionInput, metricsreplay.ModalityText, 15},
			{metricsreplay.DirectionInput, metricsreplay.ModalityAudio, 4},
			{metricsreplay.DirectionOutput, metricsreplay.ModalityAudio, 5},
			{metricsreplay.DirectionOutput, metricsreplay.ModalityText, 10},
			{metricsreplay.DirectionOutput, metricsreplay.ModalityTool, 9},
		} {
			if err := request.Recorder.Record(observation.direction, observation.modality, observation.bytes); err != nil {
				return err
			}
		}
		return nil
	}}

	service := New(Dependencies{Clock: clock, Runner: runner, Loader: &testLoader{fixture: fixture}, NewSink: factory.New})
	got, err := service.Collect(context.Background(), "fixture.json", "prompt")
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("series count = %d, want deterministic 8-series matrix", len(got))
	}
	want := []metricsreplay.Series{
		{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityAudio, ObservedDeltas: 4, ReportedTotal: 4},
		{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityText, ObservedDeltas: 15, ReportedTotal: 15},
		{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityImage},
		{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityTool},
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityAudio, ObservedDeltas: 5, ReportedTotal: 5},
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityText, ObservedDeltas: 10, ReportedTotal: 10},
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityImage},
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityTool, ObservedDeltas: 9, ReportedTotal: 9},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("series = %#v, want %#v", got, want)
	}
	if sink.closeCalls != 1 || sink.snapshotCall != 1 || factory.calls != 1 || runner.calls != 1 {
		t.Fatalf("lifecycle calls = close %d, snapshot %d, sink %d, runner %d; want 1 each", sink.closeCalls, sink.snapshotCall, factory.calls, runner.calls)
	}
}

func TestCollectIsDeterministicAcrossRepeatedRuns(t *testing.T) {
	fixture := metricsreplay.Fixture{Records: []metricsreplay.Record{
		rawRecord(1, metricsreplay.WireDirectionClientToServer, "conversation.item.create", `{"type":"conversation.item.create","item":{"content":[{"type":"input_text","text":"x"}]}}`),
	}}
	newRun := func() []metricsreplay.Series {
		sink := newTestSink()
		runner := &testRunner{run: func(_ context.Context, request metricsreplay.RunRequest) error {
			return request.Recorder.Record(metricsreplay.DirectionInput, metricsreplay.ModalityText, 1)
		}}
		factory := &testSinkFactory{sink: sink}
		service := New(Dependencies{Clock: testClock{}, Runner: runner, Loader: &testLoader{fixture: fixture}, NewSink: factory.New})
		got, err := service.Collect(context.Background(), "fixture", "")
		if err != nil {
			t.Fatalf("Collect() error = %v", err)
		}
		return got
	}
	first, second := newRun(), newRun()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated results differ: first=%#v second=%#v", first, second)
	}
}

func TestCollectRejectsMalformedOrMisattributedWireBeforeReplay(t *testing.T) {
	tests := []struct {
		name    string
		records []metricsreplay.Record
	}{
		{
			name: "empty payload",
			records: []metricsreplay.Record{{
				Sequence: 1, Direction: metricsreplay.WireDirectionServerToClient, Type: "response.text.delta", PayloadType: metricsreplay.PayloadTypeWebSocketMessage,
			}},
		},
		{
			name: "type mismatch",
			records: []metricsreplay.Record{
				rawRecord(1, metricsreplay.WireDirectionServerToClient, "response.text.delta", `{"type":"response.audio.delta","delta":"AQI="}`),
			},
		},
		{
			name: "wrong direction",
			records: []metricsreplay.Record{
				rawRecord(1, metricsreplay.WireDirectionClientToServer, "response.text.delta", `{"type":"response.text.delta","delta":"bad"}`),
			},
		},
		{
			name: "missing tool identity",
			records: []metricsreplay.Record{
				rawRecord(1, metricsreplay.WireDirectionServerToClient, "response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","delta":"{}"}`),
			},
		},
		{
			name: "duplicate tool terminal",
			records: []metricsreplay.Record{
				rawRecord(1, metricsreplay.WireDirectionServerToClient, "response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"call","arguments":"{}"}`),
				rawRecord(2, metricsreplay.WireDirectionServerToClient, "response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"call","arguments":"{}"}`),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &testRunner{}
			sink := newTestSink()
			factory := &testSinkFactory{sink: sink}
			service := New(Dependencies{Clock: testClock{}, Runner: runner, Loader: &testLoader{fixture: metricsreplay.Fixture{Records: test.records}}, NewSink: factory.New})
			_, err := service.Collect(context.Background(), "fixture", "")
			if !errors.Is(err, metricsreplay.ErrMalformedFixture) {
				t.Fatalf("Collect() error = %v, want ErrMalformedFixture", err)
			}
			if runner.calls != 0 || factory.calls != 0 {
				t.Fatalf("malformed fixture started runner=%d or sink=%d; want neither", runner.calls, factory.calls)
			}
		})
	}
}

func TestCollectPropagatesDependencyErrorsAndClosesEveryConstructedSink(t *testing.T) {
	loaderErr := errors.New("loader failed")
	runnerErr := errors.New("runner failed")
	snapshotErr := errors.New("snapshot failed")
	closeErr := errors.New("close failed")

	t.Run("loader identity", func(t *testing.T) {
		runner := &testRunner{}
		sink := newTestSink()
		factory := &testSinkFactory{sink: sink}
		service, loader := testService(metricsreplay.Fixture{}, loaderErr, runner, factory)
		_, err := service.Collect(context.Background(), "fixture", "")
		if !errors.Is(err, loaderErr) {
			t.Fatalf("error = %v, want loader identity", err)
		}
		if loader.calls != 1 || runner.calls != 0 || factory.calls != 0 {
			t.Fatalf("calls = loader %d, runner %d, sink %d; want 1,0,0", loader.calls, runner.calls, factory.calls)
		}
	})

	t.Run("runner identity and close", func(t *testing.T) {
		runner := &testRunner{run: func(context.Context, metricsreplay.RunRequest) error { return runnerErr }}
		sink := newTestSink()
		factory := &testSinkFactory{sink: sink}
		service, _ := testService(metricsreplay.Fixture{}, nil, runner, factory)
		_, err := service.Collect(context.Background(), "fixture", "")
		if !errors.Is(err, runnerErr) {
			t.Fatalf("error = %v, want runner identity", err)
		}
		if sink.closeCalls != 1 || sink.snapshotCall != 0 {
			t.Fatalf("sink lifecycle = close %d, snapshot %d; want close once and no snapshot", sink.closeCalls, sink.snapshotCall)
		}
	})

	t.Run("snapshot identity and close", func(t *testing.T) {
		runner := &testRunner{}
		sink := newTestSink()
		sink.snapshotErr = snapshotErr
		factory := &testSinkFactory{sink: sink}
		service, _ := testService(metricsreplay.Fixture{}, nil, runner, factory)
		_, err := service.Collect(context.Background(), "fixture", "")
		if !errors.Is(err, snapshotErr) {
			t.Fatalf("error = %v, want snapshot identity", err)
		}
		if sink.closeCalls != 1 || sink.snapshotCall != 1 {
			t.Fatalf("sink lifecycle = close %d, snapshot %d; want 1,1", sink.closeCalls, sink.snapshotCall)
		}
	})

	t.Run("close identity", func(t *testing.T) {
		runner := &testRunner{}
		sink := newTestSink()
		sink.closeErr = closeErr
		factory := &testSinkFactory{sink: sink}
		service, _ := testService(metricsreplay.Fixture{}, nil, runner, factory)
		_, err := service.Collect(context.Background(), "fixture", "")
		if !errors.Is(err, closeErr) {
			t.Fatalf("error = %v, want close identity", err)
		}
		if sink.closeCalls != 1 {
			t.Fatalf("close calls = %d, want exactly once", sink.closeCalls)
		}
	})
}

func TestCollectCancellationStopsBeforeSideEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loader := &testLoader{}
	runner := &testRunner{}
	sink := newTestSink()
	factory := &testSinkFactory{sink: sink}
	service := New(Dependencies{Clock: testClock{}, Runner: runner, Loader: loader, NewSink: factory.New})
	_, err := service.Collect(ctx, "fixture", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if loader.calls != 0 || runner.calls != 0 || factory.calls != 0 || sink.closeCalls != 0 {
		t.Fatalf("canceled lifecycle = loader %d, runner %d, sink %d, close %d; want all zero", loader.calls, runner.calls, factory.calls, sink.closeCalls)
	}
}

func TestCollectRejectsInvalidSnapshotAndClosesSink(t *testing.T) {
	tests := []struct {
		name   string
		series []metricsreplay.SnapshotSeries
	}{
		{
			name: "duplicate",
			series: []metricsreplay.SnapshotSeries{
				{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityText},
				{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityText},
			},
		},
		{
			name:   "unknown direction",
			series: []metricsreplay.SnapshotSeries{{Direction: "sideways", Modality: metricsreplay.ModalityText}},
		},
		{
			name:   "negative total",
			series: []metricsreplay.SnapshotSeries{{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityText, TotalBytes: -1}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := newTestSink()
			sink.series = test.series
			factory := &testSinkFactory{sink: sink}
			service, _ := testService(metricsreplay.Fixture{}, nil, &testRunner{}, factory)
			_, err := service.Collect(context.Background(), "fixture", "")
			if !errors.Is(err, metricsreplay.ErrInvalidSnapshot) {
				t.Fatalf("error = %v, want ErrInvalidSnapshot", err)
			}
			if sink.closeCalls != 1 {
				t.Fatalf("close calls = %d, want exactly once", sink.closeCalls)
			}
		})
	}
}
