package service

import (
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics"
)

type key struct {
	sourcePeer  string
	disposition roomaudiodiagnostics.Disposition
	reason      string
}

type count struct {
	bytes        uint64
	frames       uint64
	eventEmitted bool
}

type admission struct {
	sourcePeer  string
	disposition roomaudiodiagnostics.Disposition
	reason      string
	byteCount   int
	contentful  bool
}

type totals struct {
	contentful    count
	delivered     count
	backpressured count
	rejected      count
}

// Service is the private concurrent ledger behind the public contract.
type Service struct {
	participantID string
	roomID        string
	sink          roomaudiodiagnostics.Sink

	mu            sync.Mutex
	entries       map[key]count
	pending       map[string][]admission
	totals        totals
	emittedEvents int
	finishing     bool
	finished      bool
	finishOnce    sync.Once
}

func New(options roomaudiodiagnostics.Options) *Service {
	if strings.TrimSpace(options.RoomID) == "" {
		options.RoomID = "room"
	}
	return &Service{
		participantID: options.ParticipantID,
		roomID:        options.RoomID,
		sink:          options.Sink,
		entries:       make(map[key]count),
		pending:       make(map[string][]admission),
	}
}

func (s *Service) Admit(sourcePeer string, disposition roomaudiodiagnostics.Disposition, reason string, byteCount int, contentful bool) error {
	if s == nil || byteCount <= 0 {
		return nil
	}
	sourcePeer = normalizeSource(sourcePeer)
	if reason == "" {
		reason = roomaudiodiagnostics.ReasonMixerAdmitted
	}
	if !validAdmissionDisposition(disposition) {
		return roomaudiodiagnostics.InvalidDispositionError{Value: disposition}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.finishing {
		return roomaudiodiagnostics.ErrFinished
	}
	s.pending[sourcePeer] = append(s.pending[sourcePeer], admission{
		sourcePeer: sourcePeer, disposition: disposition, reason: reason,
		byteCount: byteCount, contentful: contentful,
	})
	return nil
}

func (s *Service) ResolveFrame(sourcePeers []string, byteCount int, downstreamReason string) {
	if s == nil || byteCount <= 0 || len(sourcePeers) == 0 {
		return
	}
	ids := sortedSources(sourcePeers)
	if len(ids) == 0 {
		return
	}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	emissions := make([]roomaudiodiagnostics.Record, 0, len(ids))
	for _, sourcePeer := range ids {
		emissions = append(emissions, s.resolveSourceLocked(sourcePeer, byteCount, downstreamReason)...)
	}
	s.mu.Unlock()
	s.emitAll(emissions)
}

func (s *Service) resolveSourceLocked(sourcePeer string, byteCount int, downstreamReason string) []roomaudiodiagnostics.Record {
	admissions := s.pending[sourcePeer]
	remaining := byteCount
	emissions := make([]roomaudiodiagnostics.Record, 0, len(admissions))
	for remaining > 0 && len(admissions) > 0 {
		var emitted []roomaudiodiagnostics.Record
		admissions, remaining, emitted = s.consumeAdmissionLocked(sourcePeer, admissions, remaining, downstreamReason)
		emissions = append(emissions, emitted...)
	}
	if len(admissions) == 0 {
		delete(s.pending, sourcePeer)
	} else {
		s.pending[sourcePeer] = admissions
	}
	return emissions
}

func (s *Service) consumeAdmissionLocked(sourcePeer string, admissions []admission, remaining int, downstreamReason string) ([]admission, int, []roomaudiodiagnostics.Record) {
	item := &admissions[0]
	take := item.byteCount
	if take > remaining {
		take = remaining
	}
	if take <= 0 {
		return admissions[1:], remaining, nil
	}
	emissions := make([]roomaudiodiagnostics.Record, 0, 1)
	if item.contentful {
		disposition, reason := item.disposition, item.reason
		if downstreamReason != "" {
			disposition, reason = roomaudiodiagnostics.Rejected, downstreamReason
		}
		if record, ok := s.recordLocked(sourcePeer, disposition, reason, take); ok {
			emissions = append(emissions, record)
		}
	}
	item.byteCount -= take
	if item.byteCount == 0 {
		admissions = admissions[1:]
	}
	return admissions, remaining - take, emissions
}

func (s *Service) RejectPending(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.finished || s.finishing {
		s.mu.Unlock()
		return
	}
	emissions := s.rejectPendingLocked(reason)
	s.mu.Unlock()
	s.emitAll(emissions)
}

func (s *Service) Record(sourcePeer string, disposition roomaudiodiagnostics.Disposition, reason string, byteCount int) error {
	if s == nil || byteCount <= 0 {
		return nil
	}
	sourcePeer = normalizeSource(sourcePeer)
	if reason == "" {
		reason = roomaudiodiagnostics.ReasonParticipantTerminated
	}
	var invalid error
	if !validDisposition(disposition) {
		invalid = roomaudiodiagnostics.InvalidDispositionError{Value: disposition}
		disposition = roomaudiodiagnostics.Rejected
		reason = roomaudiodiagnostics.ReasonInvalidDisposition
	}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return roomaudiodiagnostics.ErrFinished
	}
	record, ok := s.recordLocked(sourcePeer, disposition, reason, byteCount)
	s.mu.Unlock()
	if ok {
		s.emit(record)
	}
	return invalid
}

func (s *Service) Finish() {
	if s == nil {
		return
	}
	var emissions []roomaudiodiagnostics.Record
	s.finishOnce.Do(func() {
		s.mu.Lock()
		if s.finished {
			s.mu.Unlock()
			return
		}
		s.finishing = true
		emissions = append(emissions, s.rejectPendingLocked(roomaudiodiagnostics.ReasonParticipantTerminated)...)
		s.finished = true
		s.finishing = false
		emissions = append(emissions, s.summaryLocked())
		s.mu.Unlock()
	})
	s.emitAll(emissions)
}

func (s *Service) rejectPendingLocked(reason string) []roomaudiodiagnostics.Record {
	if reason == "" {
		reason = roomaudiodiagnostics.ReasonParticipantTerminated
	}
	ids := make([]string, 0, len(s.pending))
	for sourcePeer := range s.pending {
		ids = append(ids, sourcePeer)
	}
	sort.Strings(ids)
	emissions := make([]roomaudiodiagnostics.Record, 0)
	for _, sourcePeer := range ids {
		for _, item := range s.pending[sourcePeer] {
			if !item.contentful {
				continue
			}
			if record, ok := s.recordLocked(sourcePeer, roomaudiodiagnostics.Rejected, reason, item.byteCount); ok {
				emissions = append(emissions, record)
			}
		}
	}
	s.pending = make(map[string][]admission)
	return emissions
}

func (s *Service) recordLocked(sourcePeer string, disposition roomaudiodiagnostics.Disposition, reason string, byteCount int) (roomaudiodiagnostics.Record, bool) {
	if byteCount <= 0 {
		return roomaudiodiagnostics.Record{}, false
	}
	entryKey := key{sourcePeer: normalizeSource(sourcePeer), disposition: disposition, reason: reason}
	entry := s.entries[entryKey]
	entry.bytes += uint64(byteCount)
	entry.frames++
	s.entries[entryKey] = entry
	s.totals.contentful.bytes += uint64(byteCount)
	s.totals.contentful.frames++
	switch disposition {
	case roomaudiodiagnostics.Delivered:
		s.totals.delivered.bytes += uint64(byteCount)
		s.totals.delivered.frames++
	case roomaudiodiagnostics.Backpressured:
		s.totals.backpressured.bytes += uint64(byteCount)
		s.totals.backpressured.frames++
	case roomaudiodiagnostics.Rejected:
		s.totals.rejected.bytes += uint64(byteCount)
		s.totals.rejected.frames++
	}
	if entry.eventEmitted || s.emittedEvents >= roomaudiodiagnostics.MaxFirstEvents {
		return roomaudiodiagnostics.Record{}, false
	}
	entry.eventEmitted = true
	s.entries[entryKey] = entry
	fields := s.baseFieldsLocked()
	fields[roomaudiodiagnostics.FieldSourcePeer] = entryKey.sourcePeer
	fields[roomaudiodiagnostics.FieldSourcePeerID] = entryKey.sourcePeer
	fields[roomaudiodiagnostics.FieldDisposition] = string(disposition)
	fields[roomaudiodiagnostics.FieldReason] = reason
	fields[roomaudiodiagnostics.FieldByteCount] = strconv.Itoa(byteCount)
	fields[roomaudiodiagnostics.FieldFrameCount] = "1"
	fields[roomaudiodiagnostics.FieldCumulativeBytes] = strconv.FormatUint(entry.bytes, 10)
	fields[roomaudiodiagnostics.FieldCumulativeFrames] = strconv.FormatUint(entry.frames, 10)
	s.emittedEvents++
	return roomaudiodiagnostics.Record{Event: roomaudiodiagnostics.EventRoomAudioIngress, Fields: fields}, true
}

func (s *Service) summaryLocked() roomaudiodiagnostics.Record {
	sources := make([]string, 0, len(s.entries))
	seen := make(map[string]struct{}, len(s.entries))
	for entryKey := range s.entries {
		if _, ok := seen[entryKey.sourcePeer]; ok {
			continue
		}
		seen[entryKey.sourcePeer] = struct{}{}
		sources = append(sources, entryKey.sourcePeer)
	}
	sort.Strings(sources)
	sourcePeer := roomaudiodiagnostics.NoPeerSource
	reason := roomaudiodiagnostics.ReasonParticipantTerminated
	if len(sources) > 0 {
		sourcePeer = strings.Join(sources, ",")
	} else {
		reason = roomaudiodiagnostics.ReasonNoContentfulPeerAudio
	}
	totals := s.totals
	fields := s.baseFieldsLocked()
	fields[roomaudiodiagnostics.FieldDisposition] = "summary"
	fields[roomaudiodiagnostics.FieldReason] = reason
	fields[roomaudiodiagnostics.FieldSourcePeer] = sourcePeer
	fields[roomaudiodiagnostics.FieldSourcePeerID] = sourcePeer
	fields[roomaudiodiagnostics.FieldSourcePeers] = sourcePeer
	fields[roomaudiodiagnostics.FieldByteCount] = strconv.FormatUint(totals.contentful.bytes, 10)
	fields[roomaudiodiagnostics.FieldFrameCount] = strconv.FormatUint(totals.contentful.frames, 10)
	fields[roomaudiodiagnostics.FieldContentfulBytes] = strconv.FormatUint(totals.contentful.bytes, 10)
	fields[roomaudiodiagnostics.FieldContentfulFrames] = strconv.FormatUint(totals.contentful.frames, 10)
	acceptedBytes := totals.delivered.bytes + totals.backpressured.bytes
	acceptedFrames := totals.delivered.frames + totals.backpressured.frames
	fields[roomaudiodiagnostics.FieldAcceptedBytes] = strconv.FormatUint(acceptedBytes, 10)
	fields[roomaudiodiagnostics.FieldAcceptedFrames] = strconv.FormatUint(acceptedFrames, 10)
	fields[roomaudiodiagnostics.FieldDeliveredBytes] = strconv.FormatUint(totals.delivered.bytes, 10)
	fields[roomaudiodiagnostics.FieldDeliveredFrames] = strconv.FormatUint(totals.delivered.frames, 10)
	fields[roomaudiodiagnostics.FieldBackpressuredBytes] = strconv.FormatUint(totals.backpressured.bytes, 10)
	fields[roomaudiodiagnostics.FieldBackpressuredFrames] = strconv.FormatUint(totals.backpressured.frames, 10)
	fields[roomaudiodiagnostics.FieldRejectedBytes] = strconv.FormatUint(totals.rejected.bytes, 10)
	fields[roomaudiodiagnostics.FieldRejectedFrames] = strconv.FormatUint(totals.rejected.frames, 10)
	fields[roomaudiodiagnostics.FieldContentLoss] = strconv.FormatBool(totals.rejected.frames > 0)
	return roomaudiodiagnostics.Record{Event: roomaudiodiagnostics.EventRoomAudioIngressSummary, Fields: fields}
}

func (s *Service) baseFieldsLocked() map[string]string {
	return map[string]string{
		roomaudiodiagnostics.FieldRoomID:        s.roomID,
		roomaudiodiagnostics.FieldParticipantID: s.participantID,
	}
}

func (s *Service) emit(record roomaudiodiagnostics.Record) {
	if s != nil && s.sink != nil {
		s.sink.Record(record)
	}
}

func (s *Service) emitAll(records []roomaudiodiagnostics.Record) {
	for _, record := range records {
		s.emit(record)
	}
}

func normalizeSource(sourcePeer string) string {
	if strings.TrimSpace(sourcePeer) == "" {
		return roomaudiodiagnostics.MixedSource
	}
	return sourcePeer
}

func sortedSources(sourcePeers []string) []string {
	ids := append([]string(nil), sourcePeers...)
	sort.Strings(ids)
	return ids
}

func validAdmissionDisposition(disposition roomaudiodiagnostics.Disposition) bool {
	return disposition == roomaudiodiagnostics.Delivered || disposition == roomaudiodiagnostics.Backpressured
}

func validDisposition(disposition roomaudiodiagnostics.Disposition) bool {
	return validAdmissionDisposition(disposition) || disposition == roomaudiodiagnostics.Rejected
}
