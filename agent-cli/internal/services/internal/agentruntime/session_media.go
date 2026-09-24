package agentruntime

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type sessionMediaForwarder interface {
	rtcMedia() (audio.MediaEndpoints, bool)
}

func sessionMediaFromSession(session messages.Session) (audio.MediaEndpoints, bool) {
	if owner, ok := session.(audio.MediaSession); ok {
		return owner.RTCMedia(), true
	}
	if forwarder, ok := session.(sessionMediaForwarder); ok {
		return forwarder.rtcMedia()
	}
	return audio.MediaEndpoints{}, false
}

func sessionRTCMedia(session messages.Session) audio.MediaEndpoints {
	media, _ := sessionMediaFromSession(session)
	return media
}

func (s *sessionDurationAdmissionSession) RTCMedia() audio.MediaEndpoints {
	return sessionRTCMedia(s.inner)
}

func (s *sessionImageSession) RTCMedia() audio.MediaEndpoints {
	return sessionRTCMedia(s.Session)
}

func (s *sessionDirectoryRecordingSession) RTCMedia() audio.MediaEndpoints {
	return sessionRTCMedia(s.inner)
}

func (s *sessionRTCRuntimeSession) RTCMedia() audio.MediaEndpoints {
	return sessionRTCMedia(s.Session)
}
