package cli

import (
	audiocodecwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

func newTestSessionRuntimeToolService() runtimeTools.Service {
	return runtimeToolsWire.NewServiceWithAudioCodec(audiocodecwire.NewService())
}
