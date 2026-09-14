// Package service implements the private sessioncontinuation policy.
package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation"
)

var _ sessioncontinuation.Service = (*Service)(nil)

// Service is stateless; every invocation receives copied observer values.
type Service struct{}

// New constructs an inert continuation policy service.
func New() *Service { return &Service{} }

// Normalize returns a deep-owned, deterministic snapshot.
func (*Service) Normalize(input sessioncontinuation.Snapshot) sessioncontinuation.Snapshot {
	return sessioncontinuation.Snapshot{
		Unresolved: normalizeUnresolved(input.Unresolved),
		Tool:       normalizeContinuation(input.Tool),
		Image:      normalizeContinuation(input.Image),
	}
}

// Enrich joins each missing typed lifecycle cause onto primary.
func (s *Service) Enrich(primary error, input sessioncontinuation.Snapshot) error {
	snapshot := s.Normalize(input)
	imageIDs := make(map[string]struct{}, len(snapshot.Image.CallIDs))
	for _, id := range snapshot.Image.CallIDs {
		imageIDs[id] = struct{}{}
	}
	if len(imageIDs) > 0 {
		snapshot.Tool = removeIDs(snapshot.Tool, imageIDs)
	}
	result := primary
	if len(snapshot.Unresolved.CallIDs) > 0 {
		var existing *sessioncontinuation.UnresolvedToolResultsError
		if !errors.As(result, &existing) {
			result = join(result, &sessioncontinuation.UnresolvedToolResultsError{
				CallIDs:      append([]string(nil), snapshot.Unresolved.CallIDs...),
				SendStatuses: copyStatuses(snapshot.Unresolved.SendStatuses),
			})
		}
	}
	if len(snapshot.Image.CallIDs) > 0 {
		var existing *sessioncontinuation.ImageContinuationError
		if !errors.As(result, &existing) {
			result = join(result, &sessioncontinuation.ImageContinuationError{
				CallIDs:          append([]string(nil), snapshot.Image.CallIDs...),
				ProviderStatuses: copyStrings(snapshot.Image.ProviderStatuses),
				ProviderCodes:    copyStrings(snapshot.Image.ProviderCodes),
				ProviderDetails:  copyStrings(snapshot.Image.ProviderDetails),
			})
		}
	}
	if len(snapshot.Tool.CallIDs) > 0 {
		var existing *sessioncontinuation.ToolContinuationError
		if !errors.As(result, &existing) {
			result = join(result, &sessioncontinuation.ToolContinuationError{
				CallIDs:          append([]string(nil), snapshot.Tool.CallIDs...),
				ProviderStatuses: copyStrings(snapshot.Tool.ProviderStatuses),
				ProviderCodes:    copyStrings(snapshot.Tool.ProviderCodes),
				ProviderDetails:  copyStrings(snapshot.Tool.ProviderDetails),
			})
		}
	}
	return result
}

// FormatMetadata renders a stable lexical key=value list.
func (*Service) FormatMetadata(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	type entry struct{ key, value string }
	entries := make([]entry, 0, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key != "" {
			entries = append(entries, entry{key: key, value: value})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].key == entries[j].key {
			return entries[i].value < entries[j].value
		}
		return entries[i].key < entries[j].key
	})
	parts := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, exists := seen[entry.key]; exists {
			continue
		}
		seen[entry.key] = struct{}{}
		parts = append(parts, entry.key+"="+entry.value)
	}
	return strings.Join(parts, ", ")
}

// JoinAudioOutputError retains a primary cause and annotates a distinct
// output failure with the host-visible path.
func (*Service) JoinAudioOutputError(primary error, path string, outputErr error) error {
	if outputErr == nil || errors.Is(primary, outputErr) {
		return primary
	}
	return errors.Join(primary, fmt.Errorf("--audio-out %q: %w", path, outputErr))
}

func join(primary, extra error) error {
	if extra == nil {
		return primary
	}
	if primary == nil {
		return extra
	}
	return errors.Join(primary, extra)
}

func normalizeUnresolved(input sessioncontinuation.UnresolvedToolResultsSnapshot) sessioncontinuation.UnresolvedToolResultsSnapshot {
	ids := normalizeIDs(input.CallIDs)
	statuses := make(map[string]messages.SessionSendStatus, len(input.SendStatuses))
	attempts := make([]sessioncontinuation.SendAttempt, 0, len(input.SendAttempts))
	for _, attempt := range input.SendAttempts {
		id := strings.TrimSpace(attempt.CallID)
		if id == "" || attempt.Status == "" {
			continue
		}
		attempts = append(attempts, sessioncontinuation.SendAttempt{CallID: id, Status: attempt.Status})
		if attempt.Status == messages.SessionSendSucceeded {
			continue
		}
		if _, exists := statuses[id]; !exists {
			statuses[id] = attempt.Status
		}
		ids = append(ids, id)
	}
	ids = normalizeIDs(ids)
	for id, status := range copyStatusKeys(input.SendStatuses, ids) {
		if _, exists := statuses[id]; !exists {
			statuses[id] = status
		}
	}
	statuses = copyStatusKeys(statuses, ids)
	return sessioncontinuation.UnresolvedToolResultsSnapshot{CallIDs: ids, SendStatuses: statuses, SendAttempts: attempts}
}

func normalizeContinuation(input sessioncontinuation.ContinuationSnapshot) sessioncontinuation.ContinuationSnapshot {
	ids := normalizeIDs(input.CallIDs)
	return sessioncontinuation.ContinuationSnapshot{
		CallIDs:          ids,
		ProviderStatuses: copyStringKeys(input.ProviderStatuses, ids),
		ProviderCodes:    copyStringKeys(input.ProviderCodes, ids),
		ProviderDetails:  copyStringKeys(input.ProviderDetails, ids),
	}
}

func removeIDs(input sessioncontinuation.ContinuationSnapshot, excluded map[string]struct{}) sessioncontinuation.ContinuationSnapshot {
	kept := make([]string, 0, len(input.CallIDs))
	for _, id := range input.CallIDs {
		if _, found := excluded[id]; !found {
			kept = append(kept, id)
		}
	}
	input.CallIDs = kept
	input.ProviderStatuses = copyStringKeys(input.ProviderStatuses, kept)
	input.ProviderCodes = copyStringKeys(input.ProviderCodes, kept)
	input.ProviderDetails = copyStringKeys(input.ProviderDetails, kept)
	return input
}

func normalizeIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	ids := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	sort.Strings(ids)
	return ids
}

func copyStatuses(values map[string]messages.SessionSendStatus) map[string]messages.SessionSendStatus {
	result := make(map[string]messages.SessionSendStatus, len(values))
	for id, status := range values {
		result[id] = status
	}
	return result
}

func copyStatusKeys(values map[string]messages.SessionSendStatus, ids []string) map[string]messages.SessionSendStatus {
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]messages.SessionSendStatus, len(values))
	for _, key := range keys {
		id := strings.TrimSpace(key)
		if _, ok := allowed[id]; ok && values[key] != "" && values[key] != messages.SessionSendSucceeded {
			if _, exists := result[id]; !exists {
				result[id] = values[key]
			}
		}
	}
	return result
}

func copyStrings(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func copyStringKeys(values map[string]string, ids []string) map[string]string {
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]string, len(values))
	for _, key := range keys {
		normalized := strings.TrimSpace(key)
		if _, exists := allowed[normalized]; exists {
			if _, duplicate := result[normalized]; !duplicate {
				result[normalized] = values[key]
			}
		}
	}
	return result
}
