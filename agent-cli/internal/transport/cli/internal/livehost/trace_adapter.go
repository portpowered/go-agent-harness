package livehost

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	tracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func liveTraceSource(scheduler clock.Scheduler) clock.Source {
	if scheduler != nil {
		return scheduler
	}
	return clock.Real{}
}

// wrapRecorder delegates trace observation to the sessiontrace service. The
// host only supplies the invocation recorder and negotiated media rates.
func (r *publicTraceRun) wrapRecorder(inner session.LiveRecorder, request session.LiveRequest) session.LiveRecorder {
	return tracewire.NewLiveRecorder(sessiontrace.LiveRecorderOptions{
		Inner: inner, Observer: r.observer,
		InputRate: request.InputAudioSampleRate, OutputRate: request.OutputAudioSampleRate,
	})
}
