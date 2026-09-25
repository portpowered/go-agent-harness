package parity

import (
	"errors"
	"fmt"
	"io"

	"github.com/pion/webrtc/v4"
)

// watchRTCPeerState fails the replay when a peer fails or closes before the
// replay itself was closed.
func watchRTCPeerState(peer *webrtc.PeerConnection, state *rtcReplayState, role string) {
	peer.OnConnectionStateChange(func(connectionState webrtc.PeerConnectionState) {
		if connectionState != webrtc.PeerConnectionStateFailed && connectionState != webrtc.PeerConnectionStateClosed {
			return
		}
		if !state.isClosed() {
			state.fail(fmt.Errorf("RTC %s peer reached %s", role, connectionState))
		}
	})
}

// acceptRTCServerDataChannel wires the server side of the replay data channel.
func acceptRTCServerDataChannel(dataChannel *webrtc.DataChannel, state *rtcReplayState, seen, opened func()) {
	if dataChannel.Label() != parityDataChannelLabel {
		state.fail(fmt.Errorf("RTC server received unexpected data channel %q", dataChannel.Label()))
		return
	}
	seen()
	dataChannel.OnOpen(opened)
	dataChannel.OnMessage(func(message webrtc.DataChannelMessage) { state.receive(dataChannel, message) })
	dataChannel.OnError(func(dataChannelErr error) {
		state.failIfOpen(fmt.Errorf("RTC server data channel: %w", dataChannelErr))
	})
	dataChannel.OnClose(func() {
		if !state.isClosed() {
			state.fail(errors.New("RTC server data channel closed before replay completed"))
		}
	})
}

// releaseParityConn closes a parity transport during test cleanup. The test
// asserts its explicit close; this repeat close only covers early failures.
func releaseParityConn(conn io.Closer) {
	if err := conn.Close(); err != nil {
		return
	}
}
