package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// durationSidecarPath is derived from the explicit provider capture path. The
// sidecar is semantic runtime evidence and therefore lives beside, rather
// than inside, the raw provider capture. An empty capture path deliberately
// produces no sidecar.
func durationSidecarPath(capturePath string) string {
	capturePath = strings.TrimSpace(capturePath)
	if capturePath == "" {
		return ""
	}
	return strings.TrimSuffix(capturePath, filepath.Ext(capturePath)) + ".jsonl"
}

type durationSidecarMessage struct {
	Type  messages.StreamMessageType  `json:"type"`
	Value *messages.SessionCloseValue `json:"value"`
}

func (r *directoryRecorder) writeDurationSidecarTerminal(timestamp time.Time, value *messages.SessionCloseValue) error {
	if r == nil || value == nil || r.sidecarWritten {
		return nil
	}
	path := durationSidecarPath(r.options.ProviderCapturePath)
	if path == "" {
		return nil
	}
	if r.sidecar == nil {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, evidenceFileMode)
		if err != nil {
			return recordingWriteError("create duration sidecar", err)
		}
		r.sidecar = file
	}
	payload, err := json.Marshal(durationSidecarMessage{Type: messages.StreamTypeSessionClose, Value: value})
	if err != nil {
		return recordingWriteError("encode duration sidecar terminal", err)
	}
	record, err := transcript.Encode(transcript.NewRecord(
		r.sequence, timestamp, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamRuntimeEvent, payload,
	))
	if err != nil {
		return recordingWriteError("encode duration sidecar record", err)
	}
	if err := r.writeSpool(r.sidecar, record); err != nil {
		return recordingWriteError("write duration sidecar", err)
	}
	r.sidecarWritten = true
	return nil
}

// sidecarRecorder is used when a caller requests --record without a recording
// directory. It has the same lifecycle contract as the directory recorder,
// while keeping the provider capture entirely outside this semantic artifact.
type sidecarRecorder struct {
	path string

	mu              sync.Mutex
	queue           chan sidecarEvent
	done            chan struct{}
	file            *os.File
	closed          bool
	recordErr       error
	workerErr       error
	sequence        uint64
	terminalWritten bool
	writeSpool      func(*os.File, []byte) error

	finalizeOnce sync.Once
	finalizeErr  error
}

// NewSemanticSidecar creates the sibling semantic evidence writer for a raw
// provider capture path. The file is created lazily when a terminal runtime
// event is observed, so a run without a terminal does not claim completion.
func NewSemanticSidecar(providerCapturePath string) (session.LiveRecorder, error) {
	path := durationSidecarPath(providerCapturePath)
	if path == "" {
		return nil, errors.New("semantic evidence requires a provider capture path")
	}
	recorder := &sidecarRecorder{path: path, queue: make(chan sidecarEvent, 8), done: make(chan struct{}), writeSpool: writeAll}
	go recorder.run()
	return recorder, nil
}

func (r *sidecarRecorder) RecordMessage(ctx context.Context, _ session.LiveRecord) error {
	if r == nil {
		return recording.ErrLiveEvidenceClosed
	}
	return r.admitNoop(ctx)
}

func (r *sidecarRecorder) RecordAudio(ctx context.Context, _ session.LiveAudioRecord) error {
	if r == nil {
		return recording.ErrLiveEvidenceClosed
	}
	return r.admitNoop(ctx)
}

func (r *sidecarRecorder) RecordEvent(ctx context.Context, event session.LiveEvent) error {
	if r == nil {
		return recording.ErrLiveEvidenceClosed
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return recording.ErrLiveEvidenceClosed
	}
	if event.Timestamp.IsZero() {
		r.latchLocked(recordingWriteError("observe event clock", errors.New("event timestamp is unavailable")))
	}
	if event.Dropped != 0 {
		r.latchLocked(recordingWriteError("observe runtime events", fmt.Errorf("runtime dropped %d observations", event.Dropped)))
	}
	if event.Terminal == nil {
		return nil
	}
	terminal := *event.Terminal
	select {
	case r.queue <- sidecarEvent{timestamp: event.Timestamp, terminal: &terminal}:
	default:
		r.latchLocked(recordingWriteError("enqueue duration sidecar", errors.New("semantic evidence queue is full")))
	}
	return nil
}

type sidecarEvent struct {
	timestamp time.Time
	terminal  *messages.SessionCloseValue
}

func (r *sidecarRecorder) run() {
	defer close(r.done)
	for event := range r.queue {
		r.process(event)
	}
}

func (r *sidecarRecorder) process(event sidecarEvent) {
	if r.workerErr != nil || r.terminalWritten || event.terminal == nil {
		return
	}
	if r.file == nil {
		file, err := os.OpenFile(r.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, evidenceFileMode)
		if err != nil {
			r.workerErr = recordingWriteError("create duration sidecar", err)
			return
		}
		r.file = file
	}
	payload, err := json.Marshal(durationSidecarMessage{Type: messages.StreamTypeSessionClose, Value: event.terminal})
	if err != nil {
		r.workerErr = recordingWriteError("encode duration sidecar terminal", err)
		return
	}
	r.sequence++
	record, err := transcript.Encode(transcript.NewRecord(
		r.sequence, event.timestamp, transcript.PeerAgent, transcript.DirectionIn, transcript.StreamRuntimeEvent, payload,
	))
	if err != nil {
		r.workerErr = recordingWriteError("encode duration sidecar record", err)
		return
	}
	if err := r.writeSpool(r.file, record); err != nil {
		r.workerErr = recordingWriteError("write duration sidecar", err)
		return
	}
	r.terminalWritten = true
}

func (r *sidecarRecorder) admitNoop(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return recording.ErrLiveEvidenceClosed
	}
	return nil
}

func (r *sidecarRecorder) Finalize(_ context.Context, _ error) error {
	if r == nil {
		return nil
	}
	r.finalizeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		close(r.queue)
		r.mu.Unlock()
		<-r.done
		r.mu.Lock()
		recordErr := r.recordErr
		r.mu.Unlock()
		if r.file != nil {
			r.finalizeErr = errors.Join(recordErr, r.workerErr, r.file.Sync(), r.file.Close())
			return
		}
		r.finalizeErr = errors.Join(recordErr, r.workerErr)
	})
	return r.finalizeErr
}

func (r *sidecarRecorder) latchLocked(err error) {
	if err != nil && r.recordErr == nil {
		r.recordErr = err
	}
}

var _ session.LiveRecorder = (*sidecarRecorder)(nil)
