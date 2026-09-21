package agentruntime

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
)

func newTestAudioIOService() audioio.Service { return audioiowire.NewService() }
