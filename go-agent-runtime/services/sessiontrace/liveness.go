package sessiontrace

import (
	"fmt"
	"strings"
)

const (
	SilentProviderEmptyResponseClassification = "silent_provider_empty_response"
	SilentProviderTimeoutClassification       = "silent_provider_timeout"
)

type livenessErrorCode string

func (e livenessErrorCode) Error() string { return string(e) }

const (
	ErrSilentProviderEmptyResponse livenessErrorCode = "silent provider returned an empty response"
	ErrSilentProviderTimeout       livenessErrorCode = "silent provider response timed out"
)

func (e *LivenessError) Error() string {
	if e == nil {
		return "session liveness failure"
	}
	classification := strings.TrimSpace(e.Classification)
	if classification == "" {
		classification = SilentProviderEmptyResponseClassification
	}
	return fmt.Sprintf("%s: provider response produced no observable output", classification)
}

func (e *LivenessError) Unwrap() error {
	if e != nil && e.Classification == SilentProviderTimeoutClassification {
		return ErrSilentProviderTimeout
	}
	return ErrSilentProviderEmptyResponse
}
