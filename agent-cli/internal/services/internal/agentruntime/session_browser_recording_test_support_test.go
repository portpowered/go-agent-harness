package agentruntime

import (
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	runtimereplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func newTestReplayService() runtimereplay.Service { return replaywire.NewService() }

func newTestRecordingService() recording.Service { return recordingwire.NewService(clock.Real{}) }

func newTestProviderCaptureService() recording.ProviderCaptureService {
	return recordingwire.NewProviderCaptureService(clock.Real{})
}

func withTestRecordingServices(options SessionRunOptions) SessionRunOptions {
	options.RecordingService = newTestRecordingService()
	options.ProviderCaptureService = newTestProviderCaptureService()
	if options.AudioService == nil {
		options.AudioService = audioiowire.NewService()
	}
	return options
}
