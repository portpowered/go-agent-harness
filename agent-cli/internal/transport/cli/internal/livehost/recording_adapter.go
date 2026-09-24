package livehost

import (
	"errors"
	"fmt"

	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func openSemanticRecordingAdapter(destination string, service runtimeRecording.Service) (runtimeSession.LiveRecorder, error) {
	if destination == "" {
		return nil, nil
	}
	if service == nil {
		return nil, errors.New("live recording service is unavailable")
	}
	recorder, err := service.OpenLiveSemanticEvidence(destination)
	if err != nil {
		return nil, fmt.Errorf("open live semantic recording: %w", err)
	}
	return recorder, nil
}

func applyRecordingCapturePath(request *runtimeSession.LiveRequest, path string) {
	if request == nil || path == "" {
		return
	}
	request.Replay.OutputCapturePath = path
}
