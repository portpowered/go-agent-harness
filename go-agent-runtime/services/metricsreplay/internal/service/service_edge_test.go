package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
)

func fullDependencies() Dependencies {
	return Dependencies{
		Clock:   testClock{},
		Runner:  &testRunner{},
		Loader:  &testLoader{},
		NewSink: (&testSinkFactory{sink: newTestSink()}).New,
	}
}

func TestCollectRejectsMissingDependenciesAndInvalidContext(t *testing.T) {
	cases := []struct {
		name string
		deps Dependencies
		want error
	}{
		{name: "clock", deps: func() Dependencies { deps := fullDependencies(); deps.Clock = nil; return deps }(), want: metricsreplay.ErrMissingClock},
		{name: "runner", deps: func() Dependencies { deps := fullDependencies(); deps.Runner = nil; return deps }(), want: metricsreplay.ErrMissingRunner},
		{name: "loader", deps: func() Dependencies { deps := fullDependencies(); deps.Loader = nil; return deps }(), want: metricsreplay.ErrMissingLoader},
		{name: "sink factory", deps: func() Dependencies { deps := fullDependencies(); deps.NewSink = nil; return deps }(), want: metricsreplay.ErrMissingSinkFactory},
		{name: "fixture", deps: fullDependencies(), want: metricsreplay.ErrMissingFixture},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service := New(test.deps)
			fixture := "fixture"
			if test.name == "fixture" {
				fixture = "  "
			}
			_, err := service.Collect(context.Background(), fixture, "")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	t.Run("nil context", func(t *testing.T) {
		service := New(fullDependencies())
		var nilContext context.Context
		_, err := service.Collect(nilContext, "fixture", "")
		if err == nil || !errors.Is(err, context.Canceled) && err.Error() != "metrics replay requires a non-nil context" {
			t.Fatalf("error = %v, want non-nil-context diagnostic", err)
		}
	})

	t.Run("nil service", func(t *testing.T) {
		var service *Service
		_, err := service.Collect(context.Background(), "fixture", "")
		if err == nil || err.Error() != "metrics replay service is nil" {
			t.Fatalf("error = %v, want nil-service diagnostic", err)
		}
	})
}

func TestCollectRejectsNilSinkAndClosesFactoryErrorSink(t *testing.T) {
	t.Run("nil sink", func(t *testing.T) {
		factory := &testSinkFactory{}
		service := New(Dependencies{Clock: testClock{}, Runner: &testRunner{}, Loader: &testLoader{}, NewSink: factory.New})
		_, err := service.Collect(context.Background(), "fixture", "")
		if err == nil || err.Error() != "construct metrics sink: factory returned nil sink" {
			t.Fatalf("error = %v, want nil-sink diagnostic", err)
		}
	})

	t.Run("factory error", func(t *testing.T) {
		factoryErr := errors.New("factory failed")
		sink := newTestSink()
		sink.closeErr = errors.New("close after factory failure")
		factory := &testSinkFactory{sink: sink, err: factoryErr}
		service := New(Dependencies{Clock: testClock{}, Runner: &testRunner{}, Loader: &testLoader{}, NewSink: factory.New})
		_, err := service.Collect(context.Background(), "fixture", "")
		if !errors.Is(err, factoryErr) || !errors.Is(err, sink.closeErr) {
			t.Fatalf("error = %v, want factory and close identities", err)
		}
		if sink.closeCalls != 1 {
			t.Fatalf("close calls = %d, want exactly once", sink.closeCalls)
		}
	})
}

func TestBase64AccountingPreservesInvalidFallback(t *testing.T) {
	if got := decodedBase64Len("not-base64"); got != len("not-base64") {
		t.Fatalf("invalid base64 length = %d, want encoded length %d", got, len("not-base64"))
	}
	if got := decodedBase64Len("AQID"); got != 3 {
		t.Fatalf("decoded base64 length = %d, want 3", got)
	}
	if got := decodedBase64Len(""); got != 0 {
		t.Fatalf("empty base64 length = %d, want 0", got)
	}

	if err := addBytes(make(map[seriesKey]int64), seriesKey{metricsreplay.DirectionInput, metricsreplay.ModalityText}, -1); err == nil {
		t.Fatal("addBytes accepted a negative byte count")
	}
}

func TestCollectTreatsTypedNilDependenciesAsMissing(t *testing.T) {
	cases := []struct {
		name string
		deps func() Dependencies
		want error
	}{
		{
			name: "clock",
			deps: func() Dependencies {
				deps := fullDependencies()
				var clock *testClock
				deps.Clock = clock
				return deps
			},
			want: metricsreplay.ErrMissingClock,
		},
		{
			name: "runner",
			deps: func() Dependencies {
				deps := fullDependencies()
				var runner *testRunner
				deps.Runner = runner
				return deps
			},
			want: metricsreplay.ErrMissingRunner,
		},
		{
			name: "loader",
			deps: func() Dependencies {
				deps := fullDependencies()
				var loader *testLoader
				deps.Loader = loader
				return deps
			},
			want: metricsreplay.ErrMissingLoader,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.deps()).Collect(context.Background(), "fixture", "")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestInputAudioAccountingAcceptsWireShapesAndRejectsMalformedContent(t *testing.T) {
	sums := make(map[seriesKey]int64)
	if err := observeInputItem(map[string]json.RawMessage{
		"item": json.RawMessage(`{"content":[{"type":"input_audio","audio":"AQI="},{"type":"input_audio","input_audio":{"data":"AwQ="}},{"type":"other"}]}`),
	}, sums); err != nil {
		t.Fatalf("observe input audio variants: %v", err)
	}
	if got := sums[seriesKey{metricsreplay.DirectionInput, metricsreplay.ModalityAudio}]; got != 4 {
		t.Fatalf("input audio sum = %d, want 4", got)
	}

	for _, fields := range []map[string]json.RawMessage{
		{"item": json.RawMessage(`{"content":null}`)},
		{"item": json.RawMessage(`{"content":[1]}`)},
	} {
		if err := observeInputItem(fields, make(map[seriesKey]int64)); err == nil {
			t.Fatalf("observeInputItem(%s) accepted malformed content", fields["item"])
		}
	}
}

func TestObserveInputItemIgnoresNonMessageToolResults(t *testing.T) {
	sums := make(map[seriesKey]int64)
	if err := observeInputItem(map[string]json.RawMessage{
		"item": json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"tool result"}`),
	}, sums); err != nil {
		t.Fatalf("observe function call output: %v", err)
	}
	if len(sums) != 0 {
		t.Fatalf("function call output sums = %#v, want none", sums)
	}
	if err := observeInputItem(map[string]json.RawMessage{
		"item": json.RawMessage(`{"type":"message"}`),
	}, sums); err == nil {
		t.Fatal("message without content was accepted")
	}
}

func TestSelectedAudioRejectsMalformedInputAndUsesSyntheticFallback(t *testing.T) {
	for _, fields := range []map[string]json.RawMessage{
		{},
		{"audio": json.RawMessage(`null`), "synthetic_audio": json.RawMessage(`"AQI="`)},
	} {
		if _, err := selectedAudio(fields); err == nil {
			t.Fatalf("selectedAudio(%s) accepted malformed audio", fields)
		}
	}
	if got, err := selectedAudio(map[string]json.RawMessage{"synthetic_audio": json.RawMessage(`"AQI="`)}); err != nil || got != "AQI=" {
		t.Fatalf("synthetic audio = %q, %v; want AQI=", got, err)
	}
}

func TestStreamDirectionHonorsRoleAndWireFallback(t *testing.T) {
	for _, test := range []struct {
		name       string
		fields     map[string]json.RawMessage
		record     metricsreplay.Record
		transcript bool
		want       metricsreplay.Direction
		wantErr    bool
	}{
		{name: "user role", fields: map[string]json.RawMessage{"role": json.RawMessage(`"user"`)}, record: rawRecord(1, metricsreplay.WireDirectionServerToClient, "TEXT.DELTA", `{}`), want: metricsreplay.DirectionInput},
		{name: "assistant role", fields: map[string]json.RawMessage{"role": json.RawMessage(`"assistant"`)}, record: rawRecord(1, metricsreplay.WireDirectionServerToClient, "TEXT.DELTA", `{}`), want: metricsreplay.DirectionOutput},
		{name: "empty role", fields: map[string]json.RawMessage{"role": json.RawMessage(`""`)}, record: rawRecord(1, metricsreplay.WireDirectionServerToClient, "TEXT.DELTA", `{}`), want: metricsreplay.DirectionOutput},
		{name: "transcript default", fields: map[string]json.RawMessage{}, record: rawRecord(1, metricsreplay.WireDirectionServerToClient, "TRANSCRIPT.DELTA", `{}`), transcript: true, want: metricsreplay.DirectionInput},
		{name: "client fallback", fields: map[string]json.RawMessage{}, record: rawRecord(1, metricsreplay.WireDirectionClientToServer, "TEXT.DELTA", `{}`), want: metricsreplay.DirectionInput},
		{name: "bad role", fields: map[string]json.RawMessage{"role": json.RawMessage(`"system"`)}, record: rawRecord(1, metricsreplay.WireDirectionServerToClient, "TEXT.DELTA", `{}`), wantErr: true},
		{name: "bad role type", fields: map[string]json.RawMessage{"role": json.RawMessage(`3`)}, record: rawRecord(1, metricsreplay.WireDirectionServerToClient, "TEXT.DELTA", `{}`), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := streamDirection(test.fields, test.record, test.transcript)
			if test.wantErr {
				if err == nil {
					t.Fatal("streamDirection accepted invalid role")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("streamDirection() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestDecodeEventAndProjectFailClosed(t *testing.T) {
	validRaw := rawRecord(1, metricsreplay.WireDirectionServerToClient, "response.text.delta", `{"type":"response.text.delta","delta":"ok"}`)
	if event, err := decodeEvent(validRaw); err != nil || event.typeName != "response.text.delta" {
		t.Fatalf("decode valid raw = %#v, %v", event, err)
	}
	validStream := streamRecord(2, metricsreplay.WireDirectionServerToClient, "TEXT.DELTA", `{"type":"TEXT.DELTA","value":{"content":"ok"}}`)
	if event, err := decodeEvent(validStream); err != nil || event.fields["content"] == nil {
		t.Fatalf("decode valid stream = %#v, %v", event, err)
	}

	for _, record := range []metricsreplay.Record{
		{Direction: metricsreplay.WireDirectionServerToClient, Type: "x", PayloadType: "unknown", Payload: json.RawMessage(`{"type":"x"}`)},
		{Direction: metricsreplay.WireDirectionServerToClient, Type: "TEXT.DELTA", PayloadType: metricsreplay.PayloadTypeStreamMessage, Payload: json.RawMessage(`{"type":"TEXT.DELTA"}`)},
		{Direction: metricsreplay.WireDirectionServerToClient, Type: "x", Payload: json.RawMessage(`[]`)},
	} {
		if _, err := decodeEvent(record); err == nil {
			t.Fatalf("decodeEvent accepted malformed record %#v", record)
		}
	}

	validObserved := map[seriesKey]int64{{metricsreplay.DirectionInput, metricsreplay.ModalityText}: 2}
	validSnapshot := metricsreplay.Snapshot{Series: []metricsreplay.SnapshotSeries{{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityText, TotalBytes: 2}}}
	got, err := project(validSnapshot, validObserved)
	if err != nil || len(got) != len(orderedKeys()) {
		t.Fatalf("project valid = %#v, %v", got, err)
	}
	if _, err := project(validSnapshot, map[seriesKey]int64{{metricsreplay.DirectionInput, "future"}: 1}); !errors.Is(err, metricsreplay.ErrMalformedFixture) {
		t.Fatalf("project accepted unknown observed key: %v", err)
	}
	if isKnownKey(seriesKey{metricsreplay.DirectionInput, "future"}) {
		t.Fatal("unknown series key reported as known")
	}
	emittedOnly := metricsreplay.Snapshot{Series: []metricsreplay.SnapshotSeries{{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityTool, TotalBytes: 7}}}
	observedOnly := map[seriesKey]int64{{metricsreplay.DirectionInput, metricsreplay.ModalityText}: 3}
	projected, err := project(emittedOnly, observedOnly)
	if err != nil {
		t.Fatalf("projected emitted/observed-only values: %v", err)
	}
	if got := projected[1]; got.ObservedDeltas != 3 || got.ReportedTotal != 0 {
		t.Fatalf("observed-only series = %#v, want observed 3/reported 0", got)
	}
	if got := projected[7]; got.ObservedDeltas != 0 || got.ReportedTotal != 7 {
		t.Fatalf("emitted-only series = %#v, want observed 0/reported 7", got)
	}

	if err := addBytes(map[seriesKey]int64{{metricsreplay.DirectionInput, metricsreplay.ModalityText}: math.MaxInt64}, seriesKey{metricsreplay.DirectionInput, metricsreplay.ModalityText}, 1); err == nil {
		t.Fatal("addBytes accepted int64 overflow")
	}
}

func TestMalformedRecordRetainsClassificationAndCause(t *testing.T) {
	cause := errors.New("bad payload")
	record := rawRecord(7, metricsreplay.WireDirectionServerToClient, "response.text.delta", `{}`)
	err := malformedRecord(0, record, "decode", cause)
	if !errors.Is(err, metricsreplay.ErrMalformedFixture) || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want classification and cause", err)
	}
	if !reflect.DeepEqual(malformedRecord(0, record, "decode", nil), malformedRecord(0, record, "decode", nil)) {
		t.Fatal("nil-cause malformed diagnostics are not stable")
	}
}
