package probe

import (
	"context"
	"encoding/json"
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

	path, err := resolvePath(ctx, request.SourcePath)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	loaded, err := replaycapture.LoadReplayCapture(ctx, path)
	if err != nil {
		return replay.CaptureProbeObservation{}, fmt.Errorf("load replay session fixture: %w", err)
	}
	if !loaded.IntegrityVerified {
		return replay.CaptureProbeObservation{}, gatewaytesting.ErrSessionCaptureIntegrityUnavailable
	}
	if request.ValidateSource {
		if validationErrs := validateCaptureSource(path, loaded.Capture); len(validationErrs) > 0 {
			messages := make([]string, 0, len(validationErrs))
			for _, validationErr := range validationErrs {
				messages = append(messages, validationErr.Error())
			}
			return replay.CaptureProbeObservation{}, fmt.Errorf("session fixture validation failed before any probe observation: %s", strings.Join(messages, "; "))
		}
	}
	capture := loaded.Capture
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
		_ = conn.Close()
		return replayProbeReport{}, fmt.Errorf(format, args...)
	}
	for _, record := range capture.Records {
		if err := contextError(ctx); err != nil {
			return fail("replay probe canceled at sequence %d: %w", record.Sequence, err)
		}
		switch record.Direction {
		case gatewaytesting.DirectionClientToServer:
			if err := conn.WriteMessage(1, probeRecordPayload(record)); err != nil {
				return fail("replay probe outbound tick at sequence %d diverged: %w", record.Sequence, err)
			}
			report.OutboundTicks++
		case gatewaytesting.DirectionServerToClient:
			if _, _, err := conn.ReadMessage(); err != nil {
				return fail("replay probe inbound frame at sequence %d failed: %w", record.Sequence, err)
			}
			report.InboundFrames++
		default:
			return fail("replay probe event at sequence %d has invalid direction %q", record.Sequence, record.Direction)
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

func WriteCaptureDocument(ctx context.Context, resolvePath func(context.Context, string) (string, error), request replay.CaptureDocumentRequest, output io.Writer) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if output == nil {
		return fmt.Errorf("capture document output is required")
	}
	var capture gatewaytesting.SessionCapture
	switch {
	case strings.TrimSpace(request.SourcePath) != "" && strings.TrimSpace(request.SyntheticSessionID) == "":
		path, err := resolvePath(ctx, request.SourcePath)
		if err != nil {
			return err
		}
		loaded, loadErr := replaycapture.LoadReplayCapture(ctx, path)
		if loadErr != nil {
			return fmt.Errorf("load capture for export: %w", loadErr)
		}
		if !loaded.IntegrityVerified {
			return gatewaytesting.ErrSessionCaptureIntegrityUnavailable
		}
		if validationErrs := validateCaptureSource(path, loaded.Capture); len(validationErrs) > 0 {
			messages := make([]string, 0, len(validationErrs))
			for _, validationErr := range validationErrs {
				messages = append(messages, validationErr.Error())
			}
			return fmt.Errorf("session fixture validation failed before capture export: %s", strings.Join(messages, "; "))
		}
		capture = loaded.Capture
	case strings.TrimSpace(request.SourcePath) == "" && strings.TrimSpace(request.SyntheticSessionID) != "":
		capture = gatewaytesting.SessionCapture{
			Version:  gatewaytesting.SessionCaptureVersion,
			Provider: gatewaytesting.SessionProviderMetadata{Name: "probe", Model: "fixture"},
			Session: gatewaytesting.SessionMetadata{
				ID:                request.SyntheticSessionID,
				FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic,
			},
			Records: []gatewaytesting.CapturedSessionEvent{},
		}
	default:
		return fmt.Errorf("select exactly one source capture or synthetic session ID")
	}
	return json.NewEncoder(output).Encode(capture)
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
