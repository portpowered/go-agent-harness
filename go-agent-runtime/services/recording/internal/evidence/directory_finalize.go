package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"os"
	"time"
)

func (r *directoryRecorder) Finalize(ctx context.Context, runErr error) error {
	if r == nil {
		return nil
	}
	r.finalizeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		close(r.queue)
		r.mu.Unlock()
		// Finalization is mandatory evidence cleanup. A canceled invocation must
		// still drain its bounded spool and publish the partial bundle.
		<-r.done
		r.finalizeErr = r.finalize(runErr)
	})
	return r.finalizeErr
}

func (r *directoryRecorder) finalize(runErr error) error {
	// The drain worker has stopped. No observation can mutate these files or
	// snapshots, so durability operations never hold the admission mutex.
	result := errors.Join(r.recordErr, r.workerErr)
	for _, file := range []*os.File{r.client, r.agent, r.inputFile, r.outputFile, r.sidecar} {
		if file != nil {
			result = errors.Join(result, file.Sync(), file.Close())
		}
	}
	terminal := cloneTerminal(r.terminal)
	if terminal == nil {
		result = errors.Join(result, recordingWriteError("finalize terminal evidence", errors.New("terminal observation is unavailable")))
		var fallbackErr error
		terminal, fallbackErr = r.fallbackTerminal(runErr)
		result = errors.Join(result, fallbackErr)
	}
	if r.clientPath == "" || r.agentPath == "" {
		result = errors.Join(result, recordingWriteError("finalize stream evidence", errors.New("stream observations are unavailable")))
	}
	logData, logErr := r.conversation.json()
	result = errors.Join(result, logErr)
	config := r.bundleConfig(terminal, logData)
	artifact, present, artifactErr := r.providerArtifact()
	result = errors.Join(result, artifactErr)
	config.Metadata.Configuration["provider_capture"] = "unavailable"
	if present {
		config.AdditionalArtifacts = []transcript.RecordingArtifact{artifact}
		config.Metadata.Configuration["provider_capture"] = "available"
	}
	var metadataErr error
	config, metadataErr, publish := r.prepareBundleConfig(config, result)
	result = errors.Join(result, metadataErr)
	// A summary projection can be incomplete while the raw transcript/PCM
	// artifacts remain useful. Publish the valid JSONL prefix even when its
	// bounded finalization reports an error; the partial status prevents replay
	// from certifying the convenience projection as complete.
	if publish {
		result = errors.Join(result, transcript.WriteRecordingBundle(config))
	}
	result = errors.Join(result, releaseEvidenceClaim(r.lock, r.lockPath))
	if r.spool != "" {
		result = errors.Join(result, os.RemoveAll(r.spool))
	}
	return result
}

func (r *directoryRecorder) bundleConfig(terminal *transcript.RecordingTerminalSummary, logData []byte) transcript.RecordingConfig {
	return transcript.RecordingConfig{
		Destination: r.destination, ClientTranscriptPath: r.clientPath, AgentTranscriptPath: r.agentPath,
		InputSegmentPaths: r.inputPaths, OutputSegmentPaths: r.outputPaths, SessionLog: logData,
		Metadata: transcript.RecordingMetadata{
			Transport: "runtime", Model: r.options.Model,
			ClockBase:      r.options.ClockBase.UTC().Format(time.RFC3339Nano),
			WallClockStart: r.options.WallClockStart.UTC().Format(time.RFC3339Nano),
			Configuration:  map[string]string{"observation_boundary": "session-port", "provider": r.options.Provider, "session_id": r.options.SessionID, "participant_id": r.options.ParticipantID},
		},
		Terminal: terminal, Credentials: r.options.Credentials,
		BeforeCommit: func() error { return evidenceClaimOwns(r.lock, r.lockPath) },
	}
}

func evidenceClaimOwns(lock *os.File, path string) error {
	if lock == nil {
		return recording.ErrLiveEvidenceClaimed
	}
	lockInfo, err := lock.Stat()
	if err != nil {
		return fmt.Errorf("%w: inspect lock: %w", recording.ErrLiveEvidenceClaimed, err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !os.SameFile(lockInfo, pathInfo) {
		return recording.ErrLiveEvidenceClaimed
	}
	return nil
}

func cloneTerminal(value *transcript.RecordingTerminalSummary) *transcript.RecordingTerminalSummary {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (r *directoryRecorder) reserveFallbackTerminal(terminal *transcript.RecordingTerminalSummary) error {
	encoded, err := json.Marshal(terminal)
	if err != nil {
		return err
	}
	return r.budget.reserveTranscript(int64(len(encoded)), 1, true)
}

func (r *directoryRecorder) fallbackTerminal(runErr error) (*transcript.RecordingTerminalSummary, error) {
	terminal := terminalForError(runErr)
	if terminal == nil {
		return nil, nil
	}
	if err := r.reserveFallbackTerminal(terminal); err != nil {
		return nil, recordingWriteError("admit fallback terminal evidence", err)
	}
	return terminal, nil
}

func terminalForError(err error) *transcript.RecordingTerminalSummary {
	if err == nil {
		return nil
	}
	return &transcript.RecordingTerminalSummary{
		Reason: boundedEventText(err.Error()), Classification: string(messages.TerminalReasonTerminalFailure),
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceSession,
		OutputState:        messages.TerminalOutputNone,
	}
}
