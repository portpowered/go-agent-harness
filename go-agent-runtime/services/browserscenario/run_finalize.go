package browserscenario

import (
	"errors"
	"strings"
)

// Snapshot returns a defensive result snapshot. It does not finalize the run.
func (r *BrowserConversationRun) Snapshot() BrowserConversationResult {
	if r == nil {
		return BrowserConversationResult{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneBrowserConversationResult(r.result)
}

// Finalize publishes exactly one immutable result. Repeated calls return the
// same snapshot and do not permit late observations to alter it.
func (r *BrowserConversationRun) Finalize() (BrowserConversationResult, error) {
	if r == nil {
		return BrowserConversationResult{}, errors.New("browser conversation run is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finalized {
		r.finalized = true
		r.result.Finalized = true
		r.result = cloneBrowserConversationResult(r.result)
	}
	return cloneBrowserConversationResult(r.result), nil
}

func (r *BrowserConversationRun) stepForObservationLocked(stepID, observed string) (BrowserConversationStep, error) {
	if err := r.ensureMutableLocked(); err != nil {
		return BrowserConversationStep{}, err
	}
	if strings.TrimSpace(stepID) == "" {
		return BrowserConversationStep{}, browserConversationObservationError("step_id", "is required")
	}
	step, ok := r.steps[stepID]
	if !ok {
		return BrowserConversationStep{}, browserConversationObservationError("step_id", "references unknown step %q", stepID)
	}
	if strings.TrimSpace(observed) == "" {
		return BrowserConversationStep{}, browserConversationObservationError("observed_text", "must not be empty")
	}
	return step, nil
}

func (r *BrowserConversationRun) ensureMutableLocked() error {
	if r.finalized {
		return ErrBrowserConversationRunFinalized
	}
	return nil
}

func (r *BrowserConversationRun) takeSequenceLocked() uint64 {
	sequence := r.nextSeq
	r.nextSeq++
	return sequence
}
