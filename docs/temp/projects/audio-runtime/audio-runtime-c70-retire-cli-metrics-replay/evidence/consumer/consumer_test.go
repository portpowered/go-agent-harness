package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay/wire"
)

type clock struct{}

func (clock) Now() time.Time { return time.Unix(1, 0).UTC() }

type loader struct{}

func (loader) Load(context.Context, string) (metricsreplay.Fixture, error) {
	return metricsreplay.Fixture{Records: []metricsreplay.Record{{
		Direction:   metricsreplay.WireDirectionServerToClient,
		Type:        "response.text.delta",
		PayloadType: metricsreplay.PayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"response.text.delta","delta":"pong"}`),
	}}}, nil
}

type sink struct {
	total      int64
	closeCalls int
}

func (s *sink) Record(_ metricsreplay.Direction, _ metricsreplay.Modality, bytes int64) error {
	s.total += bytes
	return nil
}

func (s *sink) Snapshot() (metricsreplay.Snapshot, error) {
	return metricsreplay.Snapshot{Series: []metricsreplay.SnapshotSeries{{
		Direction:  metricsreplay.DirectionOutput,
		Modality:   metricsreplay.ModalityText,
		TotalBytes: s.total,
	}}}, nil
}

func (s *sink) Close() error {
	s.closeCalls++
	return nil
}

type runner struct {
	err error
}

func (r runner) Run(_ context.Context, request metricsreplay.RunRequest) error {
	if r.err != nil {
		return r.err
	}
	return request.Recorder.Record(metricsreplay.DirectionOutput, metricsreplay.ModalityText, 4)
}

func TestExternalConsumerUsesOnlyPublicMetricsReplaySurface(t *testing.T) {
	sink := &sink{}
	service := wire.NewService(metricsreplay.Dependencies{
		Clock:   clock{},
		Runner:  runner{},
		Loader:  loader{},
		NewSink: func() (metricsreplay.Sink, error) { return sink, nil },
	})
	got, err := service.Collect(context.Background(), "fixture", "ping")
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	want := []metricsreplay.Series{
		{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityAudio},
		{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityText},
		{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityImage},
		{Direction: metricsreplay.DirectionInput, Modality: metricsreplay.ModalityTool},
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityAudio},
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityText, ObservedDeltas: 4, ReportedTotal: 4},
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityImage},
		{Direction: metricsreplay.DirectionOutput, Modality: metricsreplay.ModalityTool},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("series = %#v, want %#v", got, want)
	}
	if sink.closeCalls != 1 {
		t.Fatalf("close calls = %d, want exactly once", sink.closeCalls)
	}
}
func TestExternalConsumerRetainsRunnerFailureIdentity(t *testing.T) {
	wantErr := errors.New("runner unavailable")
	sink := &sink{}
	service := wire.NewService(metricsreplay.Dependencies{
		Clock:   clock{},
		Runner:  runner{err: wantErr},
		Loader:  loader{},
		NewSink: func() (metricsreplay.Sink, error) { return sink, nil },
	})
	_, err := service.Collect(context.Background(), "fixture", "ping")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want runner error identity", err)
	}
	if sink.closeCalls != 1 {
		t.Fatalf("close calls = %d, want exactly once", sink.closeCalls)
	}
}
