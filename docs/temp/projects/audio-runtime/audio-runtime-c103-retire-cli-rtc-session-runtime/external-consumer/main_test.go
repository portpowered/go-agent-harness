package external_test

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession"
	rtcsessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession/wire"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestPublicWireServiceConstructsTwoIndependentRuntimeOwners(t *testing.T) {
	components := rtcsession.SessionRTCComponents{
		ResolveSignaling: func(context.Context, string) (rtc.Signaling, error) {
			return &signaling{}, nil
		},
		NewDataPlane: func(context.Context, rtc.Signaling) (rtcsession.SessionRTCDataPlane, error) {
			return &dataPlane{}, nil
		},
		OpenMediaSource: func(context.Context, string) (sharedaudio.InboundMedia, error) {
			return &inboundMedia{}, nil
		},
	}
	service := rtcsessionwire.NewService(components, nil, nil)
	if service == nil {
		t.Fatal("Wire returned a nil public service")
	}
	first, err := service.NewRuntime(rtcsession.SessionRuntimeSelection{Transport: "webrtc", SignalingEndpoint: "one", MediaSource: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.NewRuntime(rtcsession.SessionRuntimeSelection{Transport: "webrtc", SignalingEndpoint: "two", MediaSource: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("Wire service reused one runtime owner")
	}
	firstPlane, err := first.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondPlane, err := second.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if firstPlane == secondPlane {
		t.Fatal("two runtime owners shared a data plane")
	}
	if _, err := firstPlane.Dial("first", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := secondPlane.Dial("second", nil); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

type signaling struct{ closed atomic.Int32 }

func (*signaling) SendOffer(context.Context, rtc.SessionDescription) error { return nil }
func (*signaling) ReceiveOffer(context.Context) (rtc.SessionDescription, error) {
	return rtc.SessionDescription{}, nil
}
func (*signaling) SendAnswer(context.Context, rtc.SessionDescription) error { return nil }
func (*signaling) ReceiveAnswer(context.Context) (rtc.SessionDescription, error) {
	return rtc.SessionDescription{}, nil
}
func (*signaling) SendCandidate(context.Context, rtc.ICECandidate) error { return nil }
func (*signaling) ReceiveCandidate(context.Context) (rtc.ICECandidate, error) {
	return rtc.ICECandidate{}, nil
}
func (*signaling) CompleteCandidateGathering(context.Context) error { return nil }
func (*signaling) WaitCandidateGathering(context.Context) error     { return nil }
func (*signaling) Done() <-chan struct{}                            { return nil }
func (s *signaling) Close() error {
	s.closed.Add(1)
	return nil
}

type dataPlane struct {
	closed atomic.Int32
}

func (*dataPlane) Dial(string, map[string]string) (transport.Conn, error) { return connection{}, nil }
func (*dataPlane) AttachInboundMedia(context.Context, sharedaudio.InboundMedia) error {
	return nil
}
func (d *dataPlane) Close() error {
	d.closed.Add(1)
	return nil
}

type inboundMedia struct{ closed atomic.Int32 }

func (*inboundMedia) ReadFrame(context.Context) (sharedaudio.PCMFrame, error) {
	return sharedaudio.PCMFrame{}, io.EOF
}
func (m *inboundMedia) Close() error {
	m.closed.Add(1)
	return nil
}

type connection struct{}

func (connection) ReadMessage() (int, []byte, error) { return 0, nil, io.EOF }
func (connection) WriteMessage(int, []byte) error    { return nil }
func (connection) Close() error                      { return nil }
