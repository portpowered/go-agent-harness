package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	replaycapture "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/capture"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/engine"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	probeAudioAppend    = "input_audio_buffer.append"
	probeResponseCancel = "response.cancel"
)

func AnalyzeProbe(ctx context.Context, resolvePath func(context.Context, string) (string, error), request replay.CaptureProbeRequest) (replay.CaptureProbeObservation, error) {
	if err := contextError(ctx); err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	if strings.TrimSpace(request.SourcePath) == "" {
		return replay.CaptureProbeObservation{}, fmt.Errorf("replay probe capture path is required")
	}
	capture, err := loadProbeCapture(ctx, resolvePath, request)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	if request.AudioSamples != nil {
		capture, err = injectProbeAudio(capture, request)
		if err != nil {
			return replay.CaptureProbeObservation{}, err
		}
	}
	if err := contextError(ctx); err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	report, err := replayProbe(ctx, capture)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	return observeProbeCapture(report, capture), nil
}

func loadProbeCapture(ctx context.Context, resolvePath func(context.Context, string) (string, error), request replay.CaptureProbeRequest) (gatewaytesting.SessionCapture, error) {
	path, err := resolvePath(ctx, request.SourcePath)
	if err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	loaded, err := replaycapture.LoadReplayCapture(ctx, path)
	if err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("load replay session fixture: %w", err)
	}
	if !loaded.IntegrityVerified {
		return gatewaytesting.SessionCapture{}, gatewaytesting.ErrSessionCaptureIntegrityUnavailable
	}
	if request.ValidateSource {
		if err := validateProbeCapture(path, loaded.Capture, "any probe observation"); err != nil {
			return gatewaytesting.SessionCapture{}, err
		}
	}
	return loaded.Capture, nil
}

func validateProbeCapture(path string, capture gatewaytesting.SessionCapture, phase string) error {
	validationErrs := validateCaptureSource(path, capture)
	if len(validationErrs) == 0 {
		return nil
	}
	messages := make([]string, 0, len(validationErrs))
	for _, validationErr := range validationErrs {
		messages = append(messages, validationErr.Error())
	}
	return fmt.Errorf("session fixture validation failed before %s: %s", phase, strings.Join(messages, "; "))
}

func AnalyzeProbeDocument(ctx context.Context, name string, document []byte) (replay.CaptureProbeObservation, error) {
	capture, err := decodeProbeDocument(ctx, name, document)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	report, err := replayProbe(ctx, capture)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	return observeProbeCapture(report, capture), nil
}

func InspectProbeDocument(ctx context.Context, name string, document []byte) (replay.CaptureProbeObservation, error) {
	capture, err := decodeProbeDocument(ctx, name, document)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	return observeProbeCapture(replayProbeReport{}, capture), nil
}

func decodeProbeDocument(ctx context.Context, name string, document []byte) (gatewaytesting.SessionCapture, error) {
	if err := contextError(ctx); err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	if strings.TrimSpace(name) == "" {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("replay probe document name is required")
	}
	loaded, err := replaycapture.DecodeReplayCapture(name, document)
	if err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("decode provider capture: %w", err)
	}
	if !loaded.IntegrityVerified {
		return gatewaytesting.SessionCapture{}, gatewaytesting.ErrSessionCaptureIntegrityUnavailable
	}
	capture := loaded.Capture
	if validationErrs := validateCaptureSource(name, capture); len(validationErrs) > 0 {
		messages := make([]string, 0, len(validationErrs))
		for _, validationErr := range validationErrs {
			messages = append(messages, validationErr.Error())
		}
		return gatewaytesting.SessionCapture{}, fmt.Errorf("validate provider capture: %s", strings.Join(messages, "; "))
	}
	return capture, nil
}

type replayProbeReport struct {
	InboundFrames      int
	OutboundTicks      int
	Observations       []replayProbeEvent
	EndsWithDisconnect bool
}

type replayProbeEvent struct {
	Sequence  int
	Direction gatewaytesting.SessionEventDirection
	Type      string
}

func replayProbe(ctx context.Context, capture gatewaytesting.SessionCapture) (replayProbeReport, error) {
	if err := contextError(ctx); err != nil {
		return replayProbeReport{}, err
	}
	dialer, err := engine.NewWebSocketDialer(capture, false)
	if err != nil {
		return replayProbeReport{}, fmt.Errorf("open replay dialer: %w", err)
	}
	conn, err := dialer.Dial("", nil)
	if err != nil {
		return replayProbeReport{}, fmt.Errorf("open replay connection: %w", err)
	}
	report := replayProbeReport{
		EndsWithDisconnect: capture.EndsWithDisconnect,
		Observations:       make([]replayProbeEvent, 0, len(capture.Records)),
	}
	fail := func(format string, args ...any) (replayProbeReport, error) {
		closeErr := conn.Close()
		return replayProbeReport{}, errors.Join(fmt.Errorf(format, args...), closeErr)
	}
	for _, record := range capture.Records {
		if err := contextError(ctx); err != nil {
			return fail("replay probe canceled at sequence %d: %w", record.Sequence, err)
		}
		if err := replayProbeRecord(conn, record); err != nil {
			return fail("%w", err)
		}
		switch record.Direction {
		case gatewaytesting.DirectionClientToServer:
			report.OutboundTicks++
		case gatewaytesting.DirectionServerToClient:
			report.InboundFrames++
		}
		report.Observations = append(report.Observations, replayProbeEvent{
			Sequence: record.Sequence, Direction: record.Direction, Type: record.Type,
		})
	}
	if err := contextError(ctx); err != nil {
		return fail("replay probe canceled after sequence %d: %w", len(capture.Records), err)
	}
	if err := conn.Close(); err != nil {
		return replayProbeReport{}, fmt.Errorf("close replay connection: %w", err)
	}
	if err := dialer.Err(); err != nil {
		return replayProbeReport{}, fmt.Errorf("replay probe divergence: %w", err)
	}
	select {
	case <-dialer.Done():
	default:
		return replayProbeReport{}, fmt.Errorf("replay probe did not reach session end")
	}
	return report, nil
}

type probeConnection interface {
	WriteMessage(int, []byte) error
	ReadMessage() (int, []byte, error)
}

func replayProbeRecord(conn probeConnection, record gatewaytesting.CapturedSessionEvent) error {
	switch record.Direction {
	case gatewaytesting.DirectionClientToServer:
		if err := conn.WriteMessage(1, probeRecordPayload(record)); err != nil {
			return fmt.Errorf("replay probe outbound tick at sequence %d diverged: %w", record.Sequence, err)
		}
	case gatewaytesting.DirectionServerToClient:
		if _, _, err := conn.ReadMessage(); err != nil {
			return fmt.Errorf("replay probe inbound frame at sequence %d failed: %w", record.Sequence, err)
		}
	default:
		return fmt.Errorf("replay probe event at sequence %d has invalid direction %q", record.Sequence, record.Direction)
	}
	return nil
}

func WriteCaptureDocument(ctx context.Context, resolvePath func(context.Context, string) (string, error), request replay.CaptureDocumentRequest, output io.Writer) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if output == nil {
		return fmt.Errorf("capture document output is required")
	}
	capture, err := captureDocument(ctx, resolvePath, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(capture)
}

func captureDocument(ctx context.Context, resolvePath func(context.Context, string) (string, error), request replay.CaptureDocumentRequest) (gatewaytesting.SessionCapture, error) {
	hasSource := strings.TrimSpace(request.SourcePath) != ""
	hasSynthetic := strings.TrimSpace(request.SyntheticSessionID) != ""
	if hasSource == hasSynthetic {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("select exactly one source capture or synthetic session ID")
	}
	if hasSynthetic {
		return syntheticCapture(request.SyntheticSessionID), nil
	}
	path, err := resolvePath(ctx, request.SourcePath)
	if err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	loaded, err := replaycapture.LoadReplayCapture(ctx, path)
	if err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("load capture for export: %w", err)
	}
	if !loaded.IntegrityVerified {
		return gatewaytesting.SessionCapture{}, gatewaytesting.ErrSessionCaptureIntegrityUnavailable
	}
	if err := validateProbeCapture(path, loaded.Capture, "capture export"); err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	return loaded.Capture, nil
}

func syntheticCapture(sessionID string) gatewaytesting.SessionCapture {
	return gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "probe", Model: "fixture"},
		Session: gatewaytesting.SessionMetadata{
			ID:                sessionID,
			FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic,
		},
		Records: []gatewaytesting.CapturedSessionEvent{},
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("replay probe requires a context")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return nil
}
