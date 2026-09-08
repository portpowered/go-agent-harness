package lifecycle

import (
	"errors"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

func (g *roomGraph) reportParticipantError(participantID string, err error, onError func(error)) {
	if g == nil || err == nil {
		return
	}
	participant := g.participant(participantID)
	if participant == nil || participant.onMediaError == nil {
		g.fail(err, onError)
		return
	}
	g.retire(participantID)
	participant.onMediaError(err)
}

// retire removes a failed participant from every surviving mixer before its
// lifecycle cleanup runs. This keeps peer media flowing after one provider or
// device transport fails and makes the active edge set monotonic.
func (g *roomGraph) retire(participantID string) {
	if g == nil || participantID == "" {
		return
	}
	g.mu.Lock()
	if _, already := g.retired[participantID]; already {
		g.mu.Unlock()
		return
	}
	g.retired[participantID] = struct{}{}
	mixers := make(map[string]*mixer.Mixer, len(g.mixers))
	for id, target := range g.mixers {
		mixers[id] = target
	}
	var targetMixer *mixer.Mixer
	var targetQueue audio.FrameProducer
	if output := g.output(participantID); output != nil {
		targetMixer = output.mixer
		targetQueue = output.queue
	}
	g.mu.Unlock()

	for targetID, target := range mixers {
		if targetID == participantID {
			continue
		}
		g.removeRetiredInput(target, participantID)
	}
	g.closeRetiredOutput(targetQueue, targetMixer)
}

func (g *roomGraph) removeRetiredInput(target *mixer.Mixer, participantID string) {
	if g == nil || target == nil {
		return
	}
	err := target.RemoveInput(participantID)
	if err != nil && !errors.Is(err, mixer.ErrClosed) && !errors.Is(err, mixer.ErrInputMissing) {
		g.recordCleanupError(err)
	}
}

func (g *roomGraph) closeRetiredOutput(queue audio.FrameProducer, target *mixer.Mixer) {
	if g == nil {
		return
	}
	if queue != (audio.FrameProducer{}) {
		queue.Close()
	}
	if target != nil {
		if err := target.Close(); err != nil {
			g.recordCleanupError(err)
		}
	}
}

func (g *roomGraph) output(participantID string) *graphOutput {
	for _, output := range g.outputs {
		if output != nil && output.target != nil && output.target.participant.ID == participantID {
			return output
		}
	}
	return nil
}

func (g *roomGraph) participant(id string) *activeParticipant {
	for _, output := range g.outputs {
		if output != nil && output.target != nil && output.target.participant.ID == id {
			return output.target
		}
	}
	return nil
}

func (g *roomGraph) fail(err error, onError func(error)) {
	if err == nil {
		return
	}
	g.mu.Lock()
	if g.err == nil {
		g.err = err
	}
	g.mu.Unlock()
	g.cancel()
	if onError != nil {
		onError(err)
	}
}

func (g *roomGraph) Err() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

func (g *roomGraph) Close() error {
	if g == nil {
		return nil
	}
	g.close.Do(func() {
		g.cancel()
		g.workers.Wait()
		g.closeInputs()
		g.closeOutputs()
	})
	return g.Err()
}

func (g *roomGraph) closeInputs() {
	for _, inputs := range g.inputs {
		for _, input := range inputs {
			if input != nil {
				if err := input.Close(); err != nil {
					g.recordCleanupError(err)
				}
			}
		}
	}
}

func (g *roomGraph) closeOutputs() {
	for _, output := range g.outputs {
		if output == nil {
			continue
		}
		if output.queue != (audio.FrameProducer{}) {
			output.queue.Close()
		}
		if output.mixer != nil {
			if err := output.mixer.Close(); err != nil {
				g.recordCleanupError(err)
			}
		}
	}
}

func (g *roomGraph) recordCleanupError(err error) {
	if g == nil || err == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err == nil {
		g.err = err
	}
}

func (g *roomGraph) isRetired(participantID string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	_, retired := g.retired[participantID]
	g.mu.Unlock()
	return retired
}

func (g *roomGraph) sourceInputs(sourceID string) []*mixer.Input {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, retired := g.retired[sourceID]; retired {
		return nil
	}
	inputs := g.inputs[sourceID]
	result := make([]*mixer.Input, 0, len(inputs))
	for _, input := range inputs {
		if input == nil {
			continue
		}
		targetID := g.routeTo[input]
		if _, retired := g.retired[targetID]; retired {
			continue
		}
		result = append(result, input)
	}
	return result
}

func (g *roomGraph) routeTargetIDs(inputs []*mixer.Input) []string {
	if g == nil || len(inputs) == 0 {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	result := make([]string, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		if input == nil {
			continue
		}
		targetID := g.routeTo[input]
		if targetID == "" {
			continue
		}
		if _, retired := g.retired[targetID]; retired {
			continue
		}
		if _, duplicate := seen[targetID]; duplicate {
			continue
		}
		seen[targetID] = struct{}{}
		result = append(result, targetID)
	}
	return result
}

func (g *roomGraph) inputRetired(input *mixer.Input) bool {
	if g == nil || input == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	participantID := g.routeTo[input]
	_, retired := g.retired[participantID]
	return retired
}
