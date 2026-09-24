package images

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	defaultModel = "gpt-realtime-default"
	replayModel  = "gpt-realtime-replay"
	textModel    = "text-only"
)

func realtime(model string) (bool, bool) {
	switch model {
	case realtimeModel, defaultModel, replayModel:
		return true, true
	case textModel:
		return false, true
	default:
		return false, false
	}
}

func baseRequest() sessionturn.ImageCapabilityRequest {
	return sessionturn.ImageCapabilityRequest{
		Provider: " OpenAI ", Model: realtimeModel, RealtimeImageInput: realtime,
		DefaultModel:    func() (string, error) { return defaultModel, nil },
		ConfiguredModel: func(string) (*sessionturn.ImageModelMetadata, error) { return nil, nil },
	}
}

func configured(metadata *sessionturn.ImageModelMetadata, err error) func(string) (*sessionturn.ImageModelMetadata, error) {
	return func(string) (*sessionturn.ImageModelMetadata, error) { return metadata, err }
}

type capabilityCase struct {
	name      string
	edit      func(*sessionturn.ImageCapabilityRequest)
	wantModel string
	wantMIME  []string
	wantErr   error
	errModel  string
}

func capabilityCases(lookupErr error) []capabilityCase {
	return []capabilityCase{
		{name: "named", edit: func(*sessionturn.ImageCapabilityRequest) {}, wantModel: realtimeModel},
		{name: "provider mismatch", edit: func(r *sessionturn.ImageCapabilityRequest) { r.Provider, r.Model = "grok", " grok-voice " }, wantErr: sessionturn.ErrImageCapability, errModel: "grok-voice"},
		{name: "explicit empty model", edit: func(r *sessionturn.ImageCapabilityRequest) { r.Model, r.ModelProvided = " ", true }, wantErr: sessionturn.ErrImageCapability},
		{name: "replay model", edit: func(r *sessionturn.ImageCapabilityRequest) { r.Model, r.ReplayModel = "", replayModel }, wantModel: replayModel},
		{name: "default model", edit: func(r *sessionturn.ImageCapabilityRequest) { r.Model = "" }, wantModel: defaultModel},
		{name: "default model error", edit: func(r *sessionturn.ImageCapabilityRequest) {
			r.Model, r.DefaultModel = "", func() (string, error) { return "", lookupErr }
		}, wantErr: lookupErr},
		{name: "no default resolver", edit: func(r *sessionturn.ImageCapabilityRequest) { r.Model, r.DefaultModel = "", nil }, wantErr: sessionturn.ErrImageCapability},
		{name: "unknown realtime", edit: func(r *sessionturn.ImageCapabilityRequest) { r.Model = "mystery" }, wantErr: sessionturn.ErrImageCapability, errModel: "mystery"},
		{name: "text realtime", edit: func(r *sessionturn.ImageCapabilityRequest) { r.Model = textModel }, wantErr: sessionturn.ErrImageCapability, errModel: textModel},
		{name: "no realtime lookup", edit: func(r *sessionturn.ImageCapabilityRequest) { r.RealtimeImageInput = nil }, wantErr: sessionturn.ErrImageCapability, errModel: realtimeModel},
		{name: "configured modality", edit: func(r *sessionturn.ImageCapabilityRequest) {
			r.ConfiguredModel = configured(&sessionturn.ImageModelMetadata{InputModalities: []string{"text", "image"}, SupportedInputMIMETypes: []string{mimePNG}}, nil)
		}, wantModel: realtimeModel, wantMIME: []string{mimePNG}},
		{name: "configured MIME inference", edit: func(r *sessionturn.ImageCapabilityRequest) {
			r.ConfiguredModel = configured(&sessionturn.ImageModelMetadata{SupportedInputMIMETypes: []string{mimeJPEG}}, nil)
		}, wantModel: realtimeModel, wantMIME: []string{mimeJPEG}},
		{name: "configured rejection", edit: func(r *sessionturn.ImageCapabilityRequest) {
			r.ConfiguredModel = configured(&sessionturn.ImageModelMetadata{InputModalities: []string{"text"}}, nil)
		}, wantErr: sessionturn.ErrImageCapability, errModel: realtimeModel},
		{name: "configured lookup error", edit: func(r *sessionturn.ImageCapabilityRequest) { r.ConfiguredModel = configured(nil, lookupErr) }, wantErr: lookupErr},
		{name: "unconfigured lookup", edit: func(r *sessionturn.ImageCapabilityRequest) { r.ConfiguredModel = nil }, wantModel: realtimeModel},
	}
}

func TestResolveCapabilitiesRules(t *testing.T) {
	lookupErr := errors.New("load model capability metadata: broken")
	for _, tc := range capabilityCases(lookupErr) {
		t.Run(tc.name, func(t *testing.T) {
			request := baseRequest()
			tc.edit(&request)
			got, err := ResolveCapabilities(request)
			if tc.wantErr != nil {
				var typed *sessionturn.ImageCapabilityError
				if !errors.Is(err, tc.wantErr) || (errors.As(err, &typed) && typed.Model != tc.errModel) {
					t.Fatalf("error = %v, want %v for model %q", err, tc.wantErr, tc.errModel)
				}
				return
			}
			if err != nil || got.Model != tc.wantModel || !got.SupportsImageInput || len(got.SupportedInputMIMETypes) != len(tc.wantMIME) {
				t.Fatalf("capabilities = %#v, %v", got, err)
			}
		})
	}
}

func TestCloneCapabilitiesIsIndependent(t *testing.T) {
	original := capabilities()
	clone := CloneCapabilities(original)
	clone.SupportedInputMIMETypes[0] = gifType
	if original.SupportedInputMIMETypes[0] != mimePNG {
		t.Fatal("clone shared the MIME slice")
	}
}
