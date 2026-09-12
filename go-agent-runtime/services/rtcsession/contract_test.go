package rtcsession

import (
	"errors"
	"testing"
)

func TestSessionRTCRuntimeErrorPreservesCauseAndPhase(t *testing.T) {
	cause := errors.New("signaling failed")
	err := &SessionRTCRuntimeError{Phase: "resolve signaling", Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("runtime error did not preserve the underlying cause")
	}
	var typed *SessionRTCRuntimeError
	if !errors.As(err, &typed) || typed.Phase != "resolve signaling" {
		t.Fatalf("runtime error = %#v, want typed phase error", err)
	}
	if got := err.Error(); got != "WebRTC session runtime resolve signaling: signaling failed" {
		t.Fatalf("runtime error text = %q", got)
	}
	if got := (&SessionRTCRuntimeError{}).Error(); got != "WebRTC session runtime: <nil>" {
		t.Fatalf("empty runtime error text = %q", got)
	}
	if got := (*SessionRTCRuntimeError)(nil).Error(); got != nilErrorText {
		t.Fatalf("nil runtime error text = %q", got)
	}
	if got := (&SessionRTCRuntimeError{Err: cause}).Error(); got != "WebRTC session runtime: signaling failed" {
		t.Fatalf("unphased runtime error text = %q", got)
	}
	if !errors.Is(err.Unwrap(), cause) {
		t.Fatal("Unwrap did not return the original cause")
	}
	if (*SessionRTCRuntimeError)(nil).Unwrap() != nil {
		t.Fatal("nil runtime error unexpectedly unwrapped a cause")
	}
}

func TestPublicContractsExposeOpaqueSelectionsAndExplicitComponents(t *testing.T) {
	selection := SessionRuntimeSelection{Transport: "webrtc", SignalingEndpoint: "signal", MediaSource: "media"}
	if selection.Transport != "webrtc" || selection.SignalingEndpoint != "signal" || selection.MediaSource != "media" {
		t.Fatalf("selection was not retained: %#v", selection)
	}
	var service Service
	if service != nil {
		t.Fatal("zero public service interface unexpectedly non-nil")
	}
	if !errors.Is(ErrSessionRTCDataPlaneUnavailable, ErrSessionRTCDataPlaneUnavailable) {
		t.Fatal("data-plane sentinel is not comparable through errors.Is")
	}
}
