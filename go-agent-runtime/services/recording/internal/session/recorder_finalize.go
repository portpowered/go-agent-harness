package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

func (r *recorder) Finalize(ctx context.Context, runErr error) error {
	if r == nil {
		return nil
	}
	r.finalizeOnce.Do(func() {
		if r.browser != nil {
			r.browser.stop()
		}
		r.observeMu.Lock()
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		r.appendAudioArtifactsLocked()
		options := cloneSessionOptions(r.options)
		liveErr := r.live.Finalize(ctx, runErr)
		r.observeMu.Unlock()
		augmentErr := augmentBundle(options, r.browser, r.imageSnapshot(), r.toolSnapshot(), r.audioSnapshot())
		r.finalizeErr = errors.Join(r.latched(), liveErr, augmentErr)
	})
	return r.finalizeErr
}

func cloneSessionOptions(options recording.SessionOptions) recording.SessionOptions {
	options.Credentials = append([]string(nil), options.Credentials...)
	options.AdditionalArtifacts = cloneArtifacts(options.AdditionalArtifacts)
	options.Metadata.Configuration = cloneStringMap(options.Metadata.Configuration)
	return options
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copyValues := make(map[string]string, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	return copyValues
}

func (r *recorder) appendAudioArtifactsLocked() {
	for index, data := range r.inputAudio {
		r.options.AdditionalArtifacts = append(r.options.AdditionalArtifacts, transcript.RecordingArtifact{
			Path: fmt.Sprintf("audio/in-%03d.pcm", index), Data: append([]byte(nil), data...),
		})
	}
	for index, data := range r.outputAudio {
		r.options.AdditionalArtifacts = append(r.options.AdditionalArtifacts, transcript.RecordingArtifact{
			Path: fmt.Sprintf("audio/out-%03d.pcm", index), Data: append([]byte(nil), data...),
		})
	}
}

func (r *recorder) imageSnapshot() []imageEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]imageEvidence, 0, len(r.imageOrder))
	for _, id := range r.imageOrder {
		if image, ok := r.images[id]; ok {
			result = append(result, image)
		}
	}
	return result
}

type toolObservation struct {
	call      messages.ToolCall
	result    messages.ToolCallResponse
	hasResult bool
	failed    bool
}

func (r *recorder) toolSnapshot() []toolObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]toolObservation, len(r.toolOrder))
	copy(result, r.toolOrder)
	return result
}

func (r *recorder) audioSnapshot() []audioTurn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.audio.snapshot()
}

func (r *recorder) recordError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.firstErr == nil {
		r.firstErr = err
	}
	r.mu.Unlock()
}

func (r *recorder) latch(err error) error {
	r.recordError(err)
	return err
}

func (r *recorder) latched() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.firstErr
}
