package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	suffixRefresh = "_refresh"
	suffixDigest  = "_digest"
	suffixSend    = "_send"

	errNoPublisher sessionturn.Error = "session agent loop is nil"
)

// consumeEvent records one observation and reports whether it requires a
// refresh. Sequence orders events across targets; a generation never moves
// backwards for one target, even when a stale replay omitted its sequence.
func (p *Publisher) consumeEvent(event sessionturn.BrowserEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if event.Sequence != 0 && event.Sequence <= p.state.LatestEventSequence {
		return false
	}
	if event.Sequence > p.state.LatestEventSequence {
		p.state.LatestEventSequence = event.Sequence
	}
	if !catalogEvent(event.Type) {
		return false
	}
	candidate := target{browserID: event.BrowserID, targetID: event.TargetID, generation: event.Generation, sequence: event.Sequence}
	if p.staleLocked(candidate) {
		return false
	}
	if candidate.generation == 0 {
		candidate.generation = p.inheritedGenerationLocked(candidate)
	}
	p.pending = candidate
	p.hasPending = true
	return true
}

func catalogEvent(kind sessionturn.BrowserEventType) bool {
	switch kind {
	case sessionturn.BrowserEventSelected, sessionturn.BrowserEventCatalogChanged, sessionturn.BrowserEventGenerationChanged:
		return true
	default:
		return false
	}
}

func (p *Publisher) lastSuccessfulLocked() target {
	return target{
		browserID:  p.state.LastSuccessfulBrowserID,
		targetID:   p.state.LastSuccessfulTargetID,
		generation: p.state.LastSuccessfulGeneration,
	}
}

func (p *Publisher) staleLocked(candidate target) bool {
	if candidate.generation == 0 {
		return false
	}
	if p.hasPending && candidate.sameTarget(p.pending) && p.pending.generation != 0 && candidate.generation < p.pending.generation {
		return true
	}
	last := p.lastSuccessfulLocked()
	return candidate.sameTarget(last) && last.generation != 0 && candidate.generation < last.generation
}

func (p *Publisher) inheritedGenerationLocked(candidate target) uint64 {
	if p.hasPending && candidate.sameTarget(p.pending) {
		return p.pending.generation
	}
	if last := p.lastSuccessfulLocked(); candidate.sameTarget(last) {
		return last.generation
	}
	return 0
}

// refreshAndPublish reads the complete current surface, and delivers it only
// when its canonical digest changed. A failure retains the last successful
// surface and pending work.
func (p *Publisher) refreshAndPublish(ctx context.Context, phase string) error {
	definitions, err := p.refresh(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return p.fail(phase+suffixRefresh, p.latestEventSequence(), err)
	}
	canonical := Merge(p.base, definitions)
	digest, err := Digest(canonical)
	if err != nil {
		return p.fail(phase+suffixDigest, p.latestEventSequence(), err)
	}
	p.mu.Lock()
	unchanged := digest == p.state.LastSuccessfulDigest
	pending, hasPending := p.pending, p.hasPending
	p.mu.Unlock()
	if unchanged {
		p.commit(pending, hasPending, canonical, digest, false)
		return nil
	}
	if p.publish == nil {
		return p.fail(phase+suffixSend, p.latestEventSequence(), errNoPublisher)
	}
	if err := p.publish(ctx, canonical); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return p.fail(phase+suffixSend, p.latestEventSequence(), err)
	}
	p.commit(pending, hasPending, canonical, digest, true)
	return nil
}

// commit advances the last successful state only for a delivered surface.
// Clearing reconciled pending work is not a publication-state advance.
func (p *Publisher) commit(event target, hasEvent bool, definitions []messages.ToolDefinition, digest string, delivered bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if delivered {
		p.state.LastSuccessfulDefinitions = append([]messages.ToolDefinition(nil), definitions...)
		p.state.LastSuccessfulDigest = digest
		p.state.PublicationCount++
		if hasEvent {
			p.state.LastSuccessfulBrowserID = event.browserID
			p.state.LastSuccessfulTargetID = event.targetID
			p.state.LastSuccessfulGeneration = event.generation
			p.state.LastSuccessfulEventSequence = event.sequence
		}
	}
	if hasEvent && p.hasPending && p.pending == event {
		p.hasPending = false
	}
}

// Merge keeps every base definition and adds the definitions whose names do
// not collide with the base, in canonical order.
func Merge(base, definitions []messages.ToolDefinition) []messages.ToolDefinition {
	canonicalBase := messages.CanonicalToolDefinitions(base)
	canonicalDefinitions := messages.CanonicalToolDefinitions(definitions)
	if len(canonicalBase) == 0 {
		return canonicalDefinitions
	}
	merged := append([]messages.ToolDefinition(nil), canonicalBase...)
	baseNames := make(map[string]struct{}, len(canonicalBase))
	for _, definition := range canonicalBase {
		baseNames[definition.Name] = struct{}{}
	}
	for _, definition := range canonicalDefinitions {
		if _, isBase := baseNames[definition.Name]; !isBase {
			merged = append(merged, definition)
		}
	}
	return messages.CanonicalToolDefinitions(merged)
}

type digestEntry struct {
	Name             string                   `json:"name"`
	Description      string                   `json:"description"`
	Parameters       []messages.ToolParameter `json:"parameters"`
	ParameterSchema  string                   `json:"parameter_schema,omitempty"`
	ParametersClosed bool                     `json:"parameters_closed"`
}

// Digest identifies a canonical surface including complete parameter schemas.
func Digest(definitions []messages.ToolDefinition) (string, error) {
	canonical := messages.CanonicalToolDefinitions(definitions)
	payload := make([]digestEntry, 0, len(canonical))
	for _, definition := range canonical {
		payload = append(payload, digestEntry{
			Name:             definition.Name,
			Description:      definition.Description,
			Parameters:       definition.Parameters,
			ParameterSchema:  string(definition.ParameterSchema),
			ParametersClosed: definition.ParametersClosed,
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
