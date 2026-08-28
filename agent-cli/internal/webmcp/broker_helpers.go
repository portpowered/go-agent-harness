package webmcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

func findTarget(targets []Target, id TargetID) (Target, bool) {
	for _, target := range targets {
		if target.ID == id {
			return cloneTarget(target), true
		}
	}
	return Target{}, false
}

func targetPresent(targets []Target, id TargetID) bool {
	_, ok := findTarget(targets, id)
	return ok
}

func browserIDs(candidates []BrowserCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, string(candidate.ID))
	}
	sort.Strings(ids)
	return ids
}

func addressClass(candidate BrowserCandidate) string {
	if candidate.Loopback {
		return "loopback"
	}
	return "non_loopback"
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// lifecycleClassifiedError preserves the two transport/lifecycle outcomes
// whose meaning would be lost if an adapter error were wrapped as a generic
// selection or invocation failure.
func lifecycleClassifiedError(err error) (*ClassifiedError, bool) {
	var classified *ClassifiedError
	if !errors.As(err, &classified) || classified == nil {
		return nil, false
	}
	switch classified.Code {
	case ErrorBrowserDisconnected, ErrorTargetDetached:
		return classified, true
	default:
		return nil, false
	}
}

func sessionLifecycleFailure(selected *brokerSession) error {
	if selected == nil || selected.session == nil {
		return nil
	}
	if selected.lifecycleFailure != nil {
		return selected.lifecycleFailure
	}
	if failure, ok := lifecycleClassifiedError(selected.session.Err()); ok {
		return failure
	}
	return nil
}

func targetSessionLifecycleFailure(selected *brokerSession) error {
	if selected == nil || selected.session == nil {
		return nil
	}
	if failure, ok := lifecycleClassifiedError(selected.session.Err()); ok {
		return failure
	}
	return nil
}

func rememberLifecycleFailureLocked(selected *brokerSession, code ErrorCode, reason string) {
	if selected == nil || selected.lifecycleFailure != nil {
		return
	}
	if failure := targetSessionLifecycleFailure(selected); failure != nil {
		selected.lifecycleFailure = failure
		return
	}
	switch code {
	case ErrorBrowserDisconnected:
		selected.lifecycleFailure = classified(ErrorBrowserDisconnected, DefaultErrorMessage(ErrorBrowserDisconnected), map[string]any{
			"browser_id":         string(selected.context.Key.BrowserID),
			"target_id":          string(selected.context.Key.TargetID),
			"phase":              "lifecycle",
			"reconnect_required": true,
		}, nil)
	}
}

func classifiedDetails(err error) map[string]any {
	var classified *ClassifiedError
	if !errors.As(err, &classified) || classified == nil {
		return nil
	}
	return cloneDetails(classified.Details)
}

func cloneJSON(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func annotationsEqual(left, right ToolAnnotations) bool {
	return boolPointerEqual(left.ReadOnly, right.ReadOnly) &&
		boolPointerEqual(left.UntrustedContent, right.UntrustedContent) &&
		boolPointerEqual(left.AutoSubmit, right.AutoSubmit) && bytesEqual(left.Raw, right.Raw)
}

func boolPointerEqual(left, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneBrowserCandidate(candidate BrowserCandidate) BrowserCandidate {
	candidate.Diagnostics = append([]Diagnostic(nil), candidate.Diagnostics...)
	return candidate
}

func cloneBrowserCandidates(candidates []BrowserCandidate) []BrowserCandidate {
	if candidates == nil {
		return nil
	}
	result := make([]BrowserCandidate, len(candidates))
	for i, candidate := range candidates {
		result[i] = cloneBrowserCandidate(candidate)
	}
	return result
}

func cloneTarget(target Target) Target { return target }

func cloneTargets(targets []Target) []Target {
	if targets == nil {
		return nil
	}
	result := make([]Target, len(targets))
	copy(result, targets)
	return result
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type randomIDs struct{}

func (randomIDs) NewToolRef() (ToolRef, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return ToolRef(ToolRefPrefix + base64.RawURLEncoding.EncodeToString(token[:])), nil
}

func (randomIDs) NewInvocationID() (InvocationID, error) {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return InvocationID("inv-" + base64.RawURLEncoding.EncodeToString(token[:])), nil
}
