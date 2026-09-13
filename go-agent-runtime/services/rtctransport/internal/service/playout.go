package service

import (
	"fmt"

	"github.com/pion/rtp"
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
)

const (
	sequenceNumberBits      = 16
	sequenceNumberSpace     = int64(1 << sequenceNumberBits)
	sequenceNumberHalfRange = sequenceNumberSpace / 2
	sequenceNumberMask      = sequenceNumberSpace - 1
)

type inboundPlayout struct {
	track                            *InboundTrack
	packets                          map[int64]*rtp.Packet
	have, started                    bool
	baseSeq, minSeq, nextSeq, maxSeq int64
	baseTimestamp                    uint32
	ssrc                             uint32
	payloadType                      uint8
}

func (s *inboundPlayout) push(packet *rtp.Packet) error {
	if err := validateRTPVersion(packet); err != nil {
		return err
	}
	if !s.have {
		s.initialize(packet)
	}
	extended := unwrapSequence(packet.SequenceNumber, s.maxSeq)
	if s.isObsolete(extended) {
		return nil
	}
	if _, exists := s.packets[extended]; exists {
		return nil
	}
	if err := s.validatePacket(packet, extended); err != nil {
		return err
	}
	if err := s.validateWindow(packet, extended); err != nil {
		return err
	}
	if extended > s.maxSeq {
		s.maxSeq = extended
	}
	s.packets[extended] = clonePacket(packet)
	return nil
}

func validateRTPVersion(packet *rtp.Packet) error {
	if packet.Version == 2 {
		return nil
	}
	return inboundTrackError(rtctransport.ErrInvalidInboundRTPPacket, "packet", fmt.Errorf("version %d: want RTP version 2", packet.Version))
}

func (s *inboundPlayout) initialize(packet *rtp.Packet) {
	sequence := int64(packet.SequenceNumber)
	s.have, s.baseSeq, s.minSeq, s.baseTimestamp, s.maxSeq = true, sequence, sequence, packet.Timestamp, sequence
	s.ssrc, s.payloadType = packet.SSRC, packet.PayloadType
}

func (s *inboundPlayout) isObsolete(extended int64) bool {
	return s.started && extended < s.nextSeq
}

func (s *inboundPlayout) validatePacket(packet *rtp.Packet, extended int64) error {
	if packet.SSRC != s.ssrc {
		return inboundTrackError(rtctransport.ErrInvalidInboundRTPPacket, "packet", fmt.Errorf("SSRC %d changed within one audio track", packet.SSRC))
	}
	if packet.PayloadType != s.payloadType {
		return inboundTrackError(rtctransport.ErrInvalidInboundRTPPacket, "packet", fmt.Errorf("payload type %d changed within one audio track", packet.PayloadType))
	}
	expected := s.expectedTimestamp(extended)
	if packet.Timestamp != expected {
		return inboundTrackError(rtctransport.ErrImpossibleRTPProgress, "RTP progress", fmt.Errorf("sequence %d timestamp %d: want %d", packet.SequenceNumber, packet.Timestamp, expected))
	}
	return nil
}

func (s *inboundPlayout) validateWindow(packet *rtp.Packet, extended int64) error {
	if !s.started {
		return s.validateInitialWindow(packet, extended)
	}
	if extended-s.nextSeq > int64(s.track.config.jitterPackets) {
		return impossibleProgress(packet, "exceeds jitter window")
	}
	return nil
}

func (s *inboundPlayout) validateInitialWindow(packet *rtp.Packet, extended int64) error {
	if extended < s.minSeq {
		if s.minSeq-extended > int64(s.track.config.jitterPackets) {
			return impossibleProgress(packet, "exceeds initial jitter window")
		}
		s.minSeq = extended
		return nil
	}
	if extended > s.maxSeq {
		if extended-s.maxSeq > int64(s.track.config.jitterPackets) {
			return impossibleProgress(packet, "exceeds initial jitter window")
		}
		s.maxSeq = extended
	}
	return nil
}

func impossibleProgress(packet *rtp.Packet, reason string) error {
	return inboundTrackError(rtctransport.ErrImpossibleRTPProgress, "RTP progress", fmt.Errorf("sequence %d %s", packet.SequenceNumber, reason))
}

func (s *inboundPlayout) tick() error {
	if !s.started {
		s.nextSeq = s.minSeq
		s.started = true
	}
	return s.emitNext()
}

func (s *inboundPlayout) flush() error {
	if len(s.packets) == 0 {
		return nil
	}
	if !s.started {
		s.nextSeq = s.minSeq
		s.started = true
	}
	for s.nextSeq <= s.maxSeq {
		if err := s.emitNext(); err != nil {
			return err
		}
	}
	return nil
}

func (s *inboundPlayout) emitNext() error {
	packet, exists := s.packets[s.nextSeq]
	if exists {
		delete(s.packets, s.nextSeq)
	}
	if !exists && !s.started {
		return nil
	}
	var payload []byte
	if exists {
		payload = packet.Payload
	}
	samples, err := s.decode(payload, !exists)
	if err != nil {
		return err
	}
	if err := s.track.emit(samples); err != nil {
		return err
	}
	s.nextSeq++
	return nil
}

func (s *inboundPlayout) decode(payload []byte, plc bool) ([]int16, error) {
	samples, err := decodeInboundFrame(s.track.decoder, payload, plc)
	if err != nil {
		return nil, inboundTrackError(rtctransport.ErrInboundTrackDecode, "decode", err)
	}
	if len(samples) != s.track.config.codecSamples {
		return nil, inboundTrackError(rtctransport.ErrInboundTrackFrame, "decode", fmt.Errorf("got %d samples, want %d", len(samples), s.track.config.codecSamples))
	}
	owned := append([]int16(nil), samples...)
	if s.track.config.rate == rtctransport.CodecSampleRate {
		return owned, nil
	}
	resampled, err := s.track.config.resample(owned, rtctransport.CodecSampleRate, s.track.config.rate)
	if err != nil {
		return nil, inboundTrackError(rtctransport.ErrInboundTrackResample, "resample", err)
	}
	if len(resampled) != s.track.config.outputSamples {
		return nil, inboundTrackError(rtctransport.ErrInboundTrackFrame, "resample", fmt.Errorf("got %d samples, want %d", len(resampled), s.track.config.outputSamples))
	}
	return resampled, nil
}

func decodeInboundFrame(decoder rtctransport.OpusDecoder, payload []byte, plc bool) ([]int16, error) {
	if plc {
		return decoder.DecodePLC()
	}
	return decoder.Decode(payload)
}

func (s *inboundPlayout) expectedTimestamp(sequence int64) uint32 {
	return uint32(int64(s.baseTimestamp) + (sequence-s.baseSeq)*int64(s.track.config.codecSamples))
}

func inboundTrackError(kind error, operation string, err error) *rtctransport.InboundTrackError {
	return &rtctransport.InboundTrackError{Operation: operation, Kind: kind, Err: err}
}

func inboundConfigError(field string, observed any, reason string) error {
	return inboundTrackError(rtctransport.ErrInvalidInboundTrackConfig, "configuration", fmt.Errorf("%s: got %v (%s)", field, observed, reason))
}

func unwrapSequence(sequence uint16, reference int64) int64 {
	candidate := (reference &^ sequenceNumberMask) | int64(sequence)
	delta := candidate - reference
	if delta >= sequenceNumberHalfRange {
		candidate -= sequenceNumberSpace
	} else if delta < -sequenceNumberHalfRange {
		candidate += sequenceNumberSpace
	}
	return candidate
}

func clonePacket(packet *rtp.Packet) *rtp.Packet {
	cloned := *packet
	cloned.Payload = append([]byte(nil), packet.Payload...)
	cloned.CSRC = append([]uint32(nil), packet.CSRC...)
	return &cloned
}
