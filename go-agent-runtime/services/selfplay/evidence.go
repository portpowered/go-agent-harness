package selfplay

import (
	"context"
	"io"
)

const (
	AgentAWAVPath          = "agent-a.wav"
	AgentBWAVPath          = "agent-b.wav"
	AgentADiagnosticsPath  = "agent-a-diagnostics.jsonl"
	AgentBDiagnosticsPath  = "agent-b-diagnostics.jsonl"
	AgentAStreamDeltasPath = "agent-a-stream-deltas.jsonl"
	AgentBStreamDeltasPath = "agent-b-stream-deltas.jsonl"
	ManifestPath           = "run-manifest.json"
	EvidenceSchemaVersion  = 1
	EvidenceSampleRate     = 24000
)

type evidenceError string

func (e evidenceError) Error() string { return string(e) }

const (
	ErrEvidenceQuota  evidenceError = "self-play evidence quota exceeded"
	ErrEvidenceClosed evidenceError = "self-play evidence writer is closed"
)

// EvidenceLimits bounds each side's diagnostic/stream and PCM artifacts; zero selects finite defaults.
type EvidenceLimits struct {
	JSONLBytes int64
	JSONLItems int64
	WAVBytes   int64
}

const (
	DefaultJSONLBytes int64 = 64 << 20
	DefaultJSONLItems int64 = 1 << 20
	DefaultWAVBytes   int64 = 64 << 20
)

// Diagnostic is the host-neutral form of a session diagnostic record.
type Diagnostic struct {
	Event  string
	Fields map[string]string
}

// JSONLWriter is a bounded, synchronized JSONL sink owned by the runtime.
// The implementation is intentionally private to the runtime package.
type JSONLWriter interface {
	Write(value any) error
	WriteRaw(data []byte) error
	Close() error
}

// WAVWriter appends bounded PCM16 data and finalizes its RIFF header at close.
// The implementation is intentionally private to the runtime package.
type WAVWriter interface {
	Write(ctx context.Context, pcm []byte) error
	DataBytes() int64
	Path() string
	Close() error
}

// EvidenceFactory is the narrow runtime-owned authority for evidence files and
// evidence-safe transformations. Hosts retain legacy call sites through this
// port without importing the private implementation.
type EvidenceFactory interface {
	NewJSONLWriter(path string, limits EvidenceLimits) (JSONLWriter, error)
	WrapJSONLWriter(path string, writer io.Writer, limits EvidenceLimits) (JSONLWriter, error)
	NewWAVWriter(path string, sampleRate int, limits EvidenceLimits) (WAVWriter, error)
	WriteAll(writer io.Writer, data []byte) (int, error)
	CloneStringMap(fields map[string]string) map[string]string
	RedactError(value, secret string) string
	WriteAtomicJSON(path string, value any) error
}
