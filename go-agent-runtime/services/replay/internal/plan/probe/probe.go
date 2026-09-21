package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
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
	if request.ValidateSource {
		if validationErrs := gatewaytesting.ValidateSessionCaptureFile(path); len(validationErrs) > 0 {
			messages := make([]string, 0, len(validationErrs))
			for _, validationErr := range validationErrs {
				messages = append(messages, validationErr.Error())
			}
			return replay.CaptureProbeObservation{}, fmt.Errorf("session fixture validation failed before any probe observation: %s", strings.Join(messages, "; "))
		}
	}
	capture, err := gatewaytesting.LoadSessionCapture(path)
	if err != nil {
		return replay.CaptureProbeObservation{}, fmt.Errorf("load replay session fixture: %w", err)
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
	report, err := gatewaytesting.RunSessionReplayProbeFromCapture(ctx, capture)
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
	report, err := gatewaytesting.RunSessionReplayProbeFromCapture(ctx, capture)
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
	return observeProbeCapture(gatewaytesting.SessionReplayProbeReport{}, capture), nil
}

func decodeProbeDocument(ctx context.Context, name string, document []byte) (gatewaytesting.SessionCapture, error) {
	if err := contextError(ctx); err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	if strings.TrimSpace(name) == "" {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("replay probe document name is required")
	}
	var capture gatewaytesting.SessionCapture
	if err := json.Unmarshal(document, &capture); err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("decode provider capture: %w", err)
	}
	if validationErrs := gatewaytesting.ValidateSessionCapture(name, capture); len(validationErrs) > 0 {
		messages := make([]string, 0, len(validationErrs))
		for _, validationErr := range validationErrs {
			messages = append(messages, validationErr.Error())
		}
		return gatewaytesting.SessionCapture{}, fmt.Errorf("validate provider capture: %s", strings.Join(messages, "; "))
	}
	return capture, nil
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
		if validationErrs := gatewaytesting.ValidateSessionCaptureFile(path); len(validationErrs) > 0 {
			messages := make([]string, 0, len(validationErrs))
			for _, validationErr := range validationErrs {
				messages = append(messages, validationErr.Error())
			}
			return fmt.Errorf("session fixture validation failed before capture export: %s", strings.Join(messages, "; "))
		}
		capture, err = gatewaytesting.LoadSessionCapture(path)
		if err != nil {
			return fmt.Errorf("load capture for export: %w", err)
		}
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
