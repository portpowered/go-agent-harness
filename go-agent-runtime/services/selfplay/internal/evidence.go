package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const manifestSchemaVersion = 1

type evidenceFile interface {
	io.Writer
	io.Closer
	Name() string
	Sync() error
	Seek(int64, int) (int64, error)
}

type fileSystem interface {
	MkdirAll(string, os.FileMode) error
	Lstat(string) (os.FileInfo, error)
	ReadDir(string) ([]os.DirEntry, error)
	OpenFile(string, int, os.FileMode) (evidenceFile, error)
	CreateTemp(string, string) (evidenceFile, error)
	Rename(string, string) error
	Remove(string) error
	Stat(string) (os.FileInfo, error)
}

type osFileSystem struct{}

func (osFileSystem) MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }
func (osFileSystem) Lstat(path string) (os.FileInfo, error)       { return os.Lstat(path) }
func (osFileSystem) ReadDir(path string) ([]os.DirEntry, error)   { return os.ReadDir(path) }
func (osFileSystem) OpenFile(path string, flag int, mode os.FileMode) (evidenceFile, error) {
	return os.OpenFile(path, flag, mode)
}
func (osFileSystem) CreateTemp(dir, pattern string) (evidenceFile, error) {
	return os.CreateTemp(dir, pattern)
}
func (osFileSystem) Rename(oldPath, newPath string) error  { return os.Rename(oldPath, newPath) }
func (osFileSystem) Remove(path string) error              { return os.Remove(path) }
func (osFileSystem) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

type evidence struct {
	files        fileSystem
	destination  string
	startedAt    time.Time
	request      selfplay.Request
	sides        [2]*sideEvidence
	createdDir   bool
	createdPaths []string
}

type sideEvidence struct {
	role            selfplay.SideRole
	wavPath         string
	diagnosticsPath string
	streamPath      string
	wav             *wavRecorder
	diagnostics     *jsonlRecorder
	stream          *jsonlRecorder
	terminal        selfplay.SideTerminal
	terminalErr     string
}

type wavRecorder struct {
	mu        sync.Mutex
	file      evidenceFile
	path      string
	limit     int64
	dataBytes int64
	err       error
	closed    bool
}

type jsonlRecorder struct {
	mu     sync.Mutex
	file   evidenceFile
	path   string
	limit  int64
	bytes  int64
	err    error
	closed bool
}

func validateOutputTarget(files fileSystem, path string) error {
	parent := filepath.Dir(path)
	if err := files.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("prepare self-play output parent %q: %w", parent, err)
	}
	info, err := files.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect self-play output target %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %q must be a non-symlink directory", selfplay.ErrOutputTargetUnsafe, path)
	}
	entries, err := files.ReadDir(path)
	if err != nil {
		return fmt.Errorf("inspect self-play output directory %q: %w", path, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("%w: %q must be empty", selfplay.ErrOutputTargetUnsafe, path)
	}
	return nil
}

func newEvidence(files fileSystem, request selfplay.Request, startedAt time.Time) (*evidence, error) {
	entry := &evidence{files: files, destination: request.OutputDir, startedAt: startedAt, request: request}
	_, statErr := files.Lstat(request.OutputDir)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect self-play output directory %q: %w", request.OutputDir, statErr)
	}
	entry.createdDir = errors.Is(statErr, os.ErrNotExist)
	if err := files.MkdirAll(request.OutputDir, 0o700); err != nil {
		return nil, fmt.Errorf("create self-play output directory %q: %w", request.OutputDir, err)
	}
	paths := [2][3]string{
		{selfplay.SelfPlayAgentAWAVPath, selfplay.SelfPlayAgentADiagnosticsPath, selfplay.SelfPlayAgentAStreamDeltasPath},
		{selfplay.SelfPlayAgentBWAVPath, selfplay.SelfPlayAgentBDiagnosticsPath, selfplay.SelfPlayAgentBStreamDeltasPath},
	}
	roles := [2]selfplay.SideRole{selfplay.RoleCustomer, selfplay.RoleAssistant}
	for side := range paths {
		entry.sides[side] = &sideEvidence{role: roles[side], wavPath: paths[side][0], diagnosticsPath: paths[side][1], streamPath: paths[side][2], terminal: selfplay.SideNotStarted}
		wavFile, err := files.OpenFile(filepath.Join(request.OutputDir, paths[side][0]), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("create %s WAV evidence: %w", roles[side], err), entry.cleanupSetup())
		}
		entry.createdPaths = append(entry.createdPaths, filepath.Join(request.OutputDir, paths[side][0]))
		entry.sides[side].wav, err = newWAVRecorder(wavFile, paths[side][0], maxPCMBytes)
		if err != nil {
			closeErr := wavFile.Close()
			return nil, errors.Join(fmt.Errorf("create %s WAV evidence: %w", roles[side], err), closeErr, entry.cleanupSetup())
		}
		entry.sides[side].diagnostics, err = entry.newJSONL(paths[side][1], maxDiagnosticBytes)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("create %s diagnostics evidence: %w", roles[side], err), entry.cleanupSetup())
		}
		entry.sides[side].stream, err = entry.newJSONL(paths[side][2], maxStreamBytes)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("create %s stream evidence: %w", roles[side], err), entry.cleanupSetup())
		}
	}
	return entry, nil
}

func (e *evidence) newJSONL(path string, limit int64) (*jsonlRecorder, error) {
	file, err := e.files.OpenFile(filepath.Join(e.destination, path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	e.createdPaths = append(e.createdPaths, filepath.Join(e.destination, path))
	return &jsonlRecorder{file: file, path: path, limit: limit}, nil
}

func newWAVRecorder(file evidenceFile, path string, limit int64) (*wavRecorder, error) {
	header, err := wavio.PCM16Header(24000, 0)
	if err != nil {
		return nil, err
	}
	if _, err := writeAll(file, header[:]); err != nil {
		return nil, err
	}
	return &wavRecorder{file: file, path: path, limit: limit, dataBytes: 0}, nil
}

func (e *evidence) observe(side int, message messages.StreamMessage) error {
	if e == nil || side < 0 || side >= len(e.sides) || e.sides[side] == nil {
		return errors.New("self-play evidence side is unavailable")
	}
	current := e.sides[side]
	encoded, err := gatewaytesting.MarshalStreamMessage(message)
	if err != nil {
		return fmt.Errorf("marshal stream delta: %w", err)
	}
	var streamValue any
	if err := json.Unmarshal(encoded, &streamValue); err != nil {
		return fmt.Errorf("decode stream delta for redaction: %w", err)
	}
	streamValue = redactJSONValue(streamValue, e.request.APIKey)
	encoded, err = json.Marshal(streamValue)
	if err != nil {
		return fmt.Errorf("encode redacted stream delta: %w", err)
	}
	if err := current.stream.writeRaw(encoded); err != nil {
		return fmt.Errorf("write %s stream delta evidence: %w", current.role, err)
	}
	record := diagnosticRecord{Event: string(message.Type), Role: string(message.Role), ResponseID: message.ResponseID}
	if message.Type == messages.StreamTypeError {
		record.Error = redactError(streamMessageError(message), e.request.APIKey)
	}
	if err := current.diagnostics.write(record, e.request.APIKey); err != nil {
		return fmt.Errorf("write %s diagnostic evidence: %w", current.role, err)
	}
	return nil
}

func (e *evidence) observeAudio(ctx context.Context, side int, pcm []byte) error {
	if e == nil || side < 0 || side >= len(e.sides) || e.sides[side] == nil {
		return errors.New("self-play evidence side is unavailable")
	}
	if err := e.sides[side].wav.write(ctx, pcm); err != nil {
		return fmt.Errorf("write %s WAV evidence: %w", e.sides[side].role, err)
	}
	return nil
}

func (e *evidence) recordTurn(side, turns int) error {
	if e == nil || side < 0 || side >= len(e.sides) || e.sides[side] == nil {
		return errors.New("self-play evidence side is unavailable")
	}
	return e.sides[side].diagnostics.write(diagnosticRecord{Event: "turn_completed", Turn: turns}, e.request.APIKey)
}

func (e *evidence) setTerminal(side int, terminal selfplay.SideTerminal, err error, secret string) {
	if e == nil || side < 0 || side >= len(e.sides) || e.sides[side] == nil {
		return
	}
	e.sides[side].terminal = terminal
	if err != nil {
		e.sides[side].terminalErr = redactError(err.Error(), secret)
	}
}

func (e *evidence) cleanupSetup() error {
	if e == nil {
		return nil
	}
	var cleanupErr error
	for _, side := range e.sides {
		cleanupErr = errors.Join(cleanupErr, closeSetupSide(side))
	}
	return errors.Join(cleanupErr, e.removeSetupArtifacts())
}

func closeSetupSide(side *sideEvidence) error {
	if side == nil {
		return nil
	}
	return errors.Join(closeWAV(side.wav), closeJSONL(side.diagnostics), closeJSONL(side.stream))
}

func closeWAV(recorder *wavRecorder) error {
	if recorder == nil {
		return nil
	}
	return recorder.close()
}

func closeJSONL(recorder *jsonlRecorder) error {
	if recorder == nil {
		return nil
	}
	return recorder.close()
}

func (e *evidence) removeSetupArtifacts() error {
	var cleanupErr error
	for _, path := range e.createdPaths {
		if err := e.files.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove self-play setup artifact %q: %w", path, err))
		}
	}
	if e.createdDir {
		if err := e.files.Remove(e.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove self-play setup directory %q: %w", e.destination, err))
		}
	}
	return cleanupErr
}

func (e *evidence) finalize(result *selfplay.Result, runErr error, secret string) error {
	if e == nil || result == nil {
		return nil
	}
	var closeErr error
	for sideIndex, side := range e.sides {
		if side == nil {
			continue
		}
		closeErr = errors.Join(closeErr, side.wav.close(), side.diagnostics.close(), side.stream.close())
		value := sideOutcome(e.files, e.destination, side)
		if sideIndex == 0 {
			result.Customer = mergeSideResult(result.Customer, value)
		} else {
			result.Assistant = mergeSideResult(result.Assistant, value)
		}
	}
	if closeErr != nil {
		runErr = errors.Join(runErr, closeErr)
	}
	manifest := makeManifest(e, *result, runErr, secret)
	manifestPath := filepath.Join(e.destination, selfplay.SelfPlayManifestPath)
	manifestErr := atomicManifest(e.files, manifestPath, manifest)
	if manifestErr != nil {
		return errors.Join(closeErr, fmt.Errorf("write self-play manifest: %w", manifestErr))
	}
	info, statErr := e.files.Stat(manifestPath)
	if statErr != nil {
		return errors.Join(closeErr, fmt.Errorf("inspect self-play manifest: %w", statErr))
	}
	result.Manifest = selfplay.ArtifactOutcome{Path: selfplay.SelfPlayManifestPath, Bytes: info.Size(), Complete: true}
	return closeErr
}
