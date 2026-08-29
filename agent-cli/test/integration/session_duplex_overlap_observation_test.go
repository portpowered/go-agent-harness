package integration

import (
	"context"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"sync"
	"time"
)

type v8RuntimeObserver struct {
	outputBridge v8RuntimeOutputSink
	inputBridge  v8RuntimeInputSink
	turnTwoReady chan struct{}

	mu           sync.Mutex
	observations []services.SessionRuntimeObservation
	turnTwoOnce  sync.Once
}

func (o *v8RuntimeObserver) ObserveSessionRuntime(observation services.SessionRuntimeObservation) {
	if o == nil {
		return
	}
	observation.Payload = append([]byte(nil), observation.Payload...)
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
	if observation.Kind == services.SessionRuntimeObservationTurnCompleted && observation.TurnsCompleted == 2 && o.turnTwoReady != nil {
		o.turnTwoOnce.Do(func() { close(o.turnTwoReady) })
	}
	if observation.Kind == services.SessionRuntimeObservationAudioInput && o.inputBridge != nil {
		o.inputBridge.acceptRuntimeInput(observation)
	}
	if observation.Kind == services.SessionRuntimeObservationAudioOutput && o.outputBridge != nil {
		o.outputBridge.acceptRuntimeOutput(observation)
	}
}

func (o *v8RuntimeObserver) snapshot() []services.SessionRuntimeObservation {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	observations := make([]services.SessionRuntimeObservation, len(o.observations))
	for i, observation := range o.observations {
		observations[i] = observation
		observations[i].Payload = append([]byte(nil), observation.Payload...)
	}
	return observations
}

type v8StreamRecorder struct {
	mu      sync.Mutex
	records []v8StreamRecord
}

func (o *v8StreamRecorder) Observe(msg messages.StreamMessage) {
	if o == nil {
		return
	}
	record := v8StreamRecord{Type: string(msg.Type)}
	if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil {
		record.Text = value.Content
	}
	o.mu.Lock()
	o.records = append(o.records, record)
	o.mu.Unlock()
}

func (o *v8StreamRecorder) snapshot() []v8StreamRecord {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]v8StreamRecord(nil), o.records...)
}

type v8PCMWriter struct{ bridge *v8PCMBridge }

func (w v8PCMWriter) Write(data []byte) (int, error) { return w.bridge.write(data) }

type v8PCMReader struct{ bridge *v8PCMBridge }

func (r v8PCMReader) Read(data []byte) (int, error) {
	return r.bridge.read(context.Background(), data)
}

func (r v8PCMReader) ReadContext(ctx context.Context, data []byte) (int, error) {
	return r.bridge.read(ctx, data)
}

type v8ViewRecord struct {
	Order     int       `json:"order"`
	Direction string    `json:"direction"`
	TurnKey   string    `json:"turn_key,omitempty"`
	Turn      int       `json:"turn,omitempty"`
	Tick      uint64    `json:"tick"`
	Timestamp time.Time `json:"timestamp"`
	Payload   []byte    `json:"payload"`
	SHA256    string    `json:"sha256"`
	RMS       float64   `json:"rms"`
}

type v8RecordingView struct {
	Harness string
	Role    string

	mu      sync.Mutex
	records []v8ViewRecord
}

func (v *v8RecordingView) record(crossing v8Crossing, payload []byte) {
	hash, rms := v8PCMStats(payload)
	v.mu.Lock()
	v.records = append(v.records, v8ViewRecord{
		Order:     crossing.Sequence,
		Direction: crossing.Direction,
		TurnKey:   crossing.TurnKey,
		Turn:      crossing.Turn,
		Tick:      crossing.Tick,
		Timestamp: crossing.Timestamp,
		Payload:   append([]byte(nil), payload...),
		SHA256:    hash,
		RMS:       rms,
	})
	v.mu.Unlock()
}

func (v *v8RecordingView) snapshot() []v8ViewRecord {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]v8ViewRecord, len(v.records))
	for i, record := range v.records {
		out[i] = record
		out[i].Payload = append([]byte(nil), record.Payload...)
	}
	return out
}

type v8TerminalFact struct {
	Clean          bool      `json:"clean"`
	Turns          int       `json:"turns"`
	FinalTick      uint64    `json:"final_tick"`
	FinalTimestamp time.Time `json:"final_timestamp"`
	InputEOF       bool      `json:"input_eof"`
	OutputFrame    bool      `json:"output_frame"`
	Error          string    `json:"error,omitempty"`
}

type v8ViewArtifact struct {
	Harness    string         `json:"harness"`
	Role       string         `json:"role"`
	SampleRate int            `json:"sample_rate_hz"`
	Records    []v8ViewRecord `json:"records"`
	Terminal   v8TerminalFact `json:"terminal"`
}

type v8HarnessResult struct {
	Name        string
	Instruction string
	ReplayPath  string
	Err         error
	Elapsed     time.Duration
	Runtime     []services.SessionRuntimeObservation
	Stream      []v8StreamRecord
}

type v8StreamRecord struct {
	Type string
	Text string
}
