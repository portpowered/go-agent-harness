package livehost

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func replayKind(inspection *replay.CaptureInspection) runtimeSession.LiveReplayKind {
	if inspection == nil {
		return ""
	}
	switch inspection.Kind {
	case replay.CaptureKindRealtime:
		return runtimeSession.LiveReplayKindRealtime
	case replay.CaptureKindTurn:
		return runtimeSession.LiveReplayKindTurn
	default:
		return ""
	}
}

func replayTiming(value string) runtimeSession.LiveReplayTiming {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "realtime", "recorded":
		return runtimeSession.LiveReplayTimingRealtime
	case "step":
		return runtimeSession.LiveReplayTimingStep
	default:
		return runtimeSession.LiveReplayTimingFast
	}
}
