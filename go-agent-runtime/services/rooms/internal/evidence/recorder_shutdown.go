package evidence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (r *Recorder) recordError(participantID, artifact string, err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recordErr == nil {
		r.recordErr = fmt.Errorf("room evidence: %w", err)
	}
	if artifact != "" {
		r.artifactErr[filepath.ToSlash(artifact)] = err
	}
}

func (r *Recorder) closeParticipant(participant *participantRecorder) {
	if participant == nil {
		return
	}
	for path, closeFn := range map[string]func() error{
		participant.artifacts.WAV:         participant.wav.close,
		participant.artifacts.Diagnostics: participant.diagnostics.close,
		participant.artifacts.Deltas:      participant.deltas.close,
		participant.artifacts.Events:      participant.events.close,
		participant.artifacts.SentPCM:     participant.sent.close,
		participant.artifacts.ReceivedPCM: participant.received.close,
	} {
		r.recordError(participant.id, path, closeFn())
	}
}

// validateCaptures admits only provider traces actually flushed by the live
// owner. A missing trace is unavailable evidence and must make the bundle
// partial; an empty trace file is treated the same way so it cannot masquerade
// as a successful zero-audio provider session.
func (r *Recorder) validateCaptures() error {
	var validationErr error
	for _, participant := range r.participants {
		if participant == nil || participant.artifacts.Capture == "" {
			continue
		}
		path := filepath.Join(r.destination, filepath.FromSlash(participant.artifacts.Capture))
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			captureErr := fmt.Errorf("provider capture for %q is unavailable", participant.id)
			r.recordError(participant.id, participant.artifacts.Capture, captureErr)
			validationErr = errors.Join(validationErr, captureErr)
			continue
		}
		if err != nil {
			captureErr := fmt.Errorf("stat provider capture for %q: %w", participant.id, err)
			r.recordError(participant.id, participant.artifacts.Capture, captureErr)
			validationErr = errors.Join(validationErr, captureErr)
			continue
		}
		if info.IsDir() {
			captureErr := fmt.Errorf("provider capture for %q is a directory", participant.id)
			r.recordError(participant.id, participant.artifacts.Capture, captureErr)
			validationErr = errors.Join(validationErr, captureErr)
			continue
		}
		if info.Size() == 0 {
			captureErr := fmt.Errorf("provider capture for %q is empty", participant.id)
			r.recordError(participant.id, participant.artifacts.Capture, captureErr)
			validationErr = errors.Join(validationErr, captureErr)
		}
	}
	return validationErr
}

func (r *Recorder) cleanup() error {
	if r == nil {
		return nil
	}
	r.stopWorker()
	var cleanupErr error
	if r.timeline != nil {
		cleanupErr = errors.Join(cleanupErr, r.timeline.close())
	}
	for _, participant := range r.participants {
		if participant == nil {
			continue
		}
		cleanupErr = errors.Join(cleanupErr, participant.wav.close())
		cleanupErr = errors.Join(cleanupErr, participant.diagnostics.close())
		cleanupErr = errors.Join(cleanupErr, participant.deltas.close())
		cleanupErr = errors.Join(cleanupErr, participant.events.close())
		cleanupErr = errors.Join(cleanupErr, participant.sent.close())
		cleanupErr = errors.Join(cleanupErr, participant.received.close())
	}
	return errors.Join(cleanupErr, cleanupDir(r.destination))
}
