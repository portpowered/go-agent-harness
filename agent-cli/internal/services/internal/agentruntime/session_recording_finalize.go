package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func (r *sessionDirectoryRecording) RecordTerminalSummary(summary transcript.RecordingTerminalSummary) error {
	if r == nil {
		return errors.New("nil session directory recording")
	}
	if err := summary.Validate(); err != nil {
		return err
	}
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.setTerminalSummaryLocked(summary); err != nil {
		r.latchRecordErrLocked(recordingDestinationError(transcript.ErrRecordingWrite, "capture terminal summary", r.destination, err))
		return err
	}
	return nil
}

func (r *sessionDirectoryRecording) setTerminalSummaryLocked(summary transcript.RecordingTerminalSummary) error {
	if r.recordErr != nil {
		return r.recordErr
	}
	if r.terminal == nil {
		r.terminal = &summary
		return nil
	}
	if *r.terminal == summary {
		return nil
	}
	return fmt.Errorf("conflicting terminal summary: existing=%+v received=%+v", *r.terminal, summary)
}

func (r *sessionDirectoryRecording) Finalize() error {
	r.finalizeOnce.Do(func() {
		defer r.cleanupSpool()
		if r.browser != nil {
			r.browser.stop()
		}
		var browserArtifact *transcript.BrowserArtifact
		var browserErr error
		if r.browser != nil {
			browserArtifact, browserErr = r.browser.artifact()
		}
		r.eventMu.Lock()
		defer r.eventMu.Unlock()
		r.stopAndDrainSpoolWorker()
		r.mu.Lock()
		if closeErr := r.closeSpoolLocked(); closeErr != nil {
			r.latchRecordErrLocked(closeErr)
		}
		latchedRecordErr := r.recordErr
		if browserErr != nil {
			latchedRecordErr = errors.Join(
				latchedRecordErr,
				recordingDestinationError(transcript.ErrRecordingWrite, "finalize browser events", r.destination, browserErr),
			)
		}
		if r.directoryClaim != nil {
			if claimErr := r.directoryClaim.owns(); claimErr != nil {
				r.finalizeErr = recordingDestinationError(transcript.ErrRecordingDestination, "verify destination claim", r.destination, claimErr)
				r.mu.Unlock()
				return
			}
		}
		sessionLog, sessionLogErr := sessionConversationLogJSON(&r.conversation)
		if sessionLogErr != nil {
			r.finalizeErr = errors.Join(
				latchedRecordErr,
				recordingDestinationError(transcript.ErrRecordingWrite, "encode session log", r.destination, sessionLogErr),
			)
			r.mu.Unlock()
			return
		}
		var recordingStatus *transcript.RecordingStatus
		if latchedRecordErr != nil {
			recordingStatus = &transcript.RecordingStatus{
				State:  transcript.RecordingStatusPartial,
				Reason: latchedRecordErr.Error(),
			}
		}
		config := transcript.RecordingConfig{
			Destination:         r.destination,
			InputSegmentPaths:   append([]string(nil), r.inputPaths...),
			OutputSegmentPaths:  append([]string(nil), r.outputPaths...),
			SessionLog:          sessionLog,
			Metadata:            r.metadata,
			RecordingStatus:     recordingStatus,
			Credentials:         append([]string(nil), r.credentials...),
			Terminal:            cloneSessionRecordingTerminalSummary(r.terminal),
			BrowserArtifact:     browserArtifact,
			AdditionalArtifacts: copySessionRecordingArtifacts(r.imageArtifacts),
			WriteFile:           r.writeFile,
			WriteStream:         r.writeStream,
		}
		if r.clientSpoolPath() != "" {
			config.ClientTranscriptPath = r.clientSpoolPath()
		}
		if r.agentSpoolPath() != "" {
			config.AgentTranscriptPath = r.agentSpoolPath()
		}
		if r.directoryClaim != nil {
			config.BeforeCommit = r.directoryClaim.owns
		}
		timings := r.conversation.timingEntries()
		r.mu.Unlock()
		bundleErr := transcript.WriteRecordingBundle(config)
		r.finalizeErr = errors.Join(latchedRecordErr, bundleErr)
		if bundleErr != nil || len(timings) == 0 {
			return
		}
		if r.directoryClaim != nil {
			if claimErr := r.directoryClaim.owns(); claimErr != nil {
				r.finalizeErr = recordingDestinationError(transcript.ErrRecordingDestination, "verify destination claim", r.destination, claimErr)
				return
			}
		}
		// timing.json is a run-specific diagnostic beside the deterministic,
		// manifest-hashed bundle: real wall-clock turn latency legitimately
		// differs between equivalent runs, so it must never enter artifacts
		// covered by the comparability contract.
		timingBytes, timingErr := json.Marshal(timings)
		if timingErr != nil {
			r.finalizeErr = errors.Join(
				latchedRecordErr,
				recordingDestinationError(transcript.ErrRecordingWrite, "encode timing diagnostic", r.destination, timingErr),
			)
			return
		}
		timingData := append(timingBytes, 0x0a)
		var written int
		var writeErr error
		if r.writeFile != nil {
			written, writeErr = r.writeFile(filepath.Join(r.destination, "timing.json"), timingData, 0o644)
		} else {
			writeErr = os.WriteFile(filepath.Join(r.destination, "timing.json"), timingData, 0o644)
			if writeErr == nil {
				written = len(timingData)
			}
		}
		if writeErr == nil && written != len(timingData) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			r.finalizeErr = errors.Join(
				latchedRecordErr,
				recordingDestinationError(transcript.ErrRecordingWrite, "write timing diagnostic", r.destination, writeErr),
			)
		}
	})
	return r.finalizeErr
}

func (r *sessionDirectoryRecording) stopAndDrainSpoolWorker() {
	r.mu.Lock()
	queue := r.spoolQueue
	if queue != nil {
		close(queue)
		r.spoolQueue = nil
	}
	r.mu.Unlock()
	if queue != nil {
		r.spoolWG.Wait()
	}
}

func (r *sessionDirectoryRecording) clientSpoolPath() string {
	if r.clientPath == "" {
		return ""
	}
	return r.clientPath
}

func (r *sessionDirectoryRecording) agentSpoolPath() string {
	if r.agentPath == "" {
		return ""
	}
	return r.agentPath
}

func (r *sessionDirectoryRecording) closeSpoolLocked() error {
	var closeErr error
	if r.clientSpool != nil {
		closeErr = errors.Join(closeErr, r.clientSpool.Close())
	}
	if r.agentSpool != nil {
		closeErr = errors.Join(closeErr, r.agentSpool.Close())
	}
	r.clientSpool = nil
	r.agentSpool = nil
	return closeErr
}

func (r *sessionDirectoryRecording) cleanupSpool() {
	if r.spoolDir == "" {
		return
	}
	_ = os.RemoveAll(r.spoolDir)
	r.spoolDir = ""
}

func cloneSessionRecordingTerminalSummary(summary *transcript.RecordingTerminalSummary) *transcript.RecordingTerminalSummary {
	if summary == nil {
		return nil
	}
	clone := *summary
	return &clone
}

func copySessionRecordingArtifacts(artifacts []transcript.RecordingArtifact) []transcript.RecordingArtifact {
	copyOf := make([]transcript.RecordingArtifact, len(artifacts))
	for index, artifact := range artifacts {
		copyOf[index] = transcript.RecordingArtifact{
			Path:   artifact.Path,
			Data:   append([]byte(nil), artifact.Data...),
			SHA256: artifact.SHA256,
		}
	}
	return copyOf
}
