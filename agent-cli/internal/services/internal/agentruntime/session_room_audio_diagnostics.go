package agentruntime

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
)

const (
	// SessionDiagnosticEventRoomAudioIngress is the bounded first-observation
	// record for contentful peer audio entering one participant's room input.
	SessionDiagnosticEventRoomAudioIngress = "room_audio_ingress"
	// SessionDiagnosticEventRoomAudioIngressSummary is emitted once for every
	// participant after its inbound mixer work has stopped. It is emitted even
	// when no peer supplied contentful audio, so zero input is diagnosable.
	SessionDiagnosticEventRoomAudioIngressSummary = "room_audio_ingress_summary"

	SessionDiagnosticFieldRoomID              = "room_id"
	SessionDiagnosticFieldParticipantID       = "participant_id"
	SessionDiagnosticFieldSourcePeer          = "source_peer"
	SessionDiagnosticFieldSourcePeerID        = "source_peer_id"
	SessionDiagnosticFieldSourcePeers         = "source_peers"
	SessionDiagnosticFieldDisposition         = "disposition"
	SessionDiagnosticFieldReason              = "reason"
	SessionDiagnosticFieldByteCount           = "byte_count"
	SessionDiagnosticFieldFrameCount          = "frame_count"
	SessionDiagnosticFieldCumulativeBytes     = "cumulative_bytes"
	SessionDiagnosticFieldCumulativeFrames    = "cumulative_frames"
	SessionDiagnosticFieldContentfulBytes     = "contentful_bytes"
	SessionDiagnosticFieldContentfulFrames    = "contentful_frames"
	SessionDiagnosticFieldAcceptedBytes       = "accepted_bytes"
	SessionDiagnosticFieldAcceptedFrames      = "accepted_frames"
	SessionDiagnosticFieldDeliveredBytes      = "delivered_bytes"
	SessionDiagnosticFieldDeliveredFrames     = "delivered_frames"
	SessionDiagnosticFieldBackpressuredBytes  = "backpressured_bytes"
	SessionDiagnosticFieldBackpressuredFrames = "backpressured_frames"
	SessionDiagnosticFieldRejectedBytes       = "rejected_bytes"
	SessionDiagnosticFieldRejectedFrames      = "rejected_frames"
	SessionDiagnosticFieldContentLoss         = "content_loss"
)

const (
	RoomAudioIngressDelivered     RoomAudioIngressDisposition = "delivered"
	RoomAudioIngressBackpressured RoomAudioIngressDisposition = "backpressured"
	RoomAudioIngressRejected      RoomAudioIngressDisposition = "rejected"

	roomAudioIngressDispositionDelivered     = RoomAudioIngressDelivered
	roomAudioIngressDispositionBackpressured = RoomAudioIngressBackpressured
	roomAudioIngressDispositionRejected      = RoomAudioIngressRejected

	roomAudioIngressReasonNoContentfulPeerAudio     = "no_contentful_peer_audio"
	roomAudioIngressReasonParticipantTerminated     = "participant_terminated"
	roomAudioIngressReasonParticipantOutputRejected = "participant_output_rejected"
	roomAudioIngressReasonProviderInputRejected     = "provider_input_rejected"
	roomAudioIngressMixedSource                     = "room-mix"
	roomAudioIngressNoPeer                          = "none"
	roomAudioIngressMaxFirstEvents                  = 32
)

// RoomAudioIngressDisposition is the stable disposition vocabulary used by
// room ingress diagnostics. Backpressured is still admitted audio; it records
// that bounded mixer capacity made the write wait before acceptance.
type RoomAudioIngressDisposition string

type roomAudioIngressKey struct {
	sourcePeer  string
	disposition RoomAudioIngressDisposition
	reason      string
}

type roomAudioIngressCount struct {
	bytes        uint64
	frames       uint64
	eventEmitted bool
}

type roomAudioIngressAdmission struct {
	sourcePeer  string
	disposition RoomAudioIngressDisposition
	reason      string
	byteCount   int
	contentful  bool
}

type roomAudioIngressContribution struct {
	sourcePeer  string
	disposition RoomAudioIngressDisposition
	reason      string
	byteCount   int
	contentful  bool
}

type roomAudioIngressTotals struct {
	contentful    roomAudioIngressCount
	delivered     roomAudioIngressCount
	backpressured roomAudioIngressCount
	rejected      roomAudioIngressCount
}

// roomAudioIngressLedger aggregates contentful frame dispositions for one
// target participant. It emits at most one first-observation record per
// source/disposition/reason key, followed by one cumulative summary. This
// keeps a broken peer or closed input from flooding the diagnostic sinks while
// retaining exact byte and frame totals.
type roomAudioIngressLedger struct {
	participantID string
	roomID        string
	sink          SessionDiagnosticSink

	mu            sync.Mutex
	entries       map[roomAudioIngressKey]roomAudioIngressCount
	pending       map[string][]roomAudioIngressAdmission
	totals        roomAudioIngressTotals
	emittedEvents int
	finishOnce    sync.Once
}

func newRoomAudioIngressLedger(participantID string, sink SessionDiagnosticSink) *roomAudioIngressLedger {
	return &roomAudioIngressLedger{
		participantID: participantID,
		roomID:        RoomStreamRoomParticipantID,
		sink:          sink,
		entries:       make(map[roomAudioIngressKey]roomAudioIngressCount),
		pending:       make(map[string][]roomAudioIngressAdmission),
	}
}

func newRoomParticipantIngress(plan *roomParticipantPlan, opts RoomRunOptions, evidence *roomEvidence) *roomAudioIngressLedger {
	if plan == nil {
		return nil
	}
	participantID := plan.manifest.ID
	sink := combineRoomDiagnosticSinks(roomParticipantDiagnosticSinks(
		plan,
		opts,
		evidenceParticipant(evidence, participantID),
		roomParticipantStreamSink(opts.Stream, participantID),
	)...)
	return newRoomAudioIngressLedger(participantID, sink)
}

func notifyRoomParticipantMixerReady(opts RoomRunOptions, participantID string, mixer *room.PCM16Mixer) {
	if opts.onParticipantMixerReady != nil {
		opts.onParticipantMixerReady(participantID, mixer)
	}
}

// admit records the source identity and mixer admission disposition only after
// the mixer has appended the complete PCM chunk. It deliberately does not emit
// a diagnostic yet: the chunk is still pending until a mixed frame reaches the
// participant's downstream sink. Silent chunks are retained as non-contentful
// byte ranges so a later contentful chunk cannot be attributed to an earlier
// mixed frame.
//
// The mixer invokes this callback while holding its input lock. Keep this
// method bounded and independent of the mixer so admission and source metadata
// cannot be separated by a concurrent mix tick.
func (l *roomAudioIngressLedger) admit(sourcePeer string, disposition RoomAudioIngressDisposition, reason string, byteCount int, contentful bool) {
	if l == nil || byteCount <= 0 {
		return
	}
	if strings.TrimSpace(sourcePeer) == "" {
		sourcePeer = roomAudioIngressMixedSource
	}
	if reason == "" {
		_, reason = roomAudioIngressDisposition(room.PCM16WriteDelivered, nil)
	}
	switch disposition {
	case roomAudioIngressDispositionDelivered, roomAudioIngressDispositionBackpressured:
	default:
		return
	}
	l.mu.Lock()
	l.pending[sourcePeer] = append(l.pending[sourcePeer], roomAudioIngressAdmission{
		sourcePeer:  sourcePeer,
		disposition: disposition,
		reason:      reason,
		byteCount:   byteCount,
		contentful:  contentful,
	})
	l.mu.Unlock()
}

// resolveFrame consumes the source-side byte ranges represented by one mixed
// cadence frame and records their downstream outcome. A failed downstream
// send turns every contributing admitted range into a rejection while keeping
// the original peer identity; it must never collapse into the synthetic
// room-mix source.
func (l *roomAudioIngressLedger) resolveFrame(sourcePeers []string, byteCount int, downstreamReason string) {
	if l == nil || byteCount <= 0 {
		return
	}
	contributions := l.consumeFrame(sourcePeers, byteCount)
	for _, contribution := range contributions {
		if !contribution.contentful {
			continue
		}
		disposition := contribution.disposition
		reason := contribution.reason
		if downstreamReason != "" {
			disposition = roomAudioIngressDispositionRejected
			reason = downstreamReason
		}
		l.record(contribution.sourcePeer, disposition, reason, contribution.byteCount)
	}
}

// consumeFrame mirrors PCM16Mixer's per-input bounded drain. A mixer frame
// consumes at most frameBytes from each source input, so the ledger consumes
// the same amount from that source's FIFO admissions and preserves source and
// disposition across mixed output frames.
func (l *roomAudioIngressLedger) consumeFrame(sourcePeers []string, frameBytes int) []roomAudioIngressContribution {
	if l == nil || frameBytes <= 0 || len(sourcePeers) == 0 {
		return nil
	}
	ids := append([]string(nil), sourcePeers...)
	sort.Strings(ids)
	contributions := make([]roomAudioIngressContribution, 0, len(ids))
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, sourcePeer := range ids {
		admissions := l.pending[sourcePeer]
		remaining := frameBytes
		for remaining > 0 && len(admissions) > 0 {
			admission := &admissions[0]
			take := admission.byteCount
			if take > remaining {
				take = remaining
			}
			if take <= 0 {
				admissions = admissions[1:]
				continue
			}
			contributions = append(contributions, roomAudioIngressContribution{
				sourcePeer:  admission.sourcePeer,
				disposition: admission.disposition,
				reason:      admission.reason,
				byteCount:   take,
				contentful:  admission.contentful,
			})
			admission.byteCount -= take
			remaining -= take
			if admission.byteCount == 0 {
				admissions = admissions[1:]
			}
		}
		if len(admissions) == 0 {
			delete(l.pending, sourcePeer)
		} else {
			l.pending[sourcePeer] = admissions
		}
	}
	return contributions
}

func (l *roomAudioIngressLedger) rejectPending(reason string) {
	if l == nil {
		return
	}
	if reason == "" {
		reason = roomAudioIngressReasonParticipantTerminated
	}
	l.mu.Lock()
	pending := make([]roomAudioIngressAdmission, 0)
	for sourcePeer, admissions := range l.pending {
		for _, admission := range admissions {
			admission.sourcePeer = sourcePeer
			pending = append(pending, admission)
		}
	}
	l.pending = make(map[string][]roomAudioIngressAdmission)
	l.mu.Unlock()
	for _, admission := range pending {
		if admission.contentful {
			l.record(admission.sourcePeer, roomAudioIngressDispositionRejected, reason, admission.byteCount)
		}
	}
}

func (l *roomAudioIngressLedger) record(sourcePeer string, disposition RoomAudioIngressDisposition, reason string, byteCount int) {
	if l == nil || byteCount <= 0 {
		return
	}
	if strings.TrimSpace(sourcePeer) == "" {
		sourcePeer = roomAudioIngressMixedSource
	}
	if reason == "" {
		reason = roomAudioIngressReasonParticipantTerminated
	}
	switch disposition {
	case roomAudioIngressDispositionDelivered, roomAudioIngressDispositionBackpressured, roomAudioIngressDispositionRejected:
	default:
		disposition = roomAudioIngressDispositionRejected
		reason = "invalid_disposition"
	}

	key := roomAudioIngressKey{sourcePeer: sourcePeer, disposition: disposition, reason: reason}
	var firstRecord *SessionDiagnosticRecord
	l.mu.Lock()
	entry := l.entries[key]
	entry.bytes += uint64(byteCount)
	entry.frames++
	l.entries[key] = entry
	l.totals.contentful.bytes += uint64(byteCount)
	l.totals.contentful.frames++
	var total *roomAudioIngressCount
	switch disposition {
	case roomAudioIngressDispositionDelivered:
		total = &l.totals.delivered
	case roomAudioIngressDispositionBackpressured:
		total = &l.totals.backpressured
	case roomAudioIngressDispositionRejected:
		total = &l.totals.rejected
	}
	total.bytes += uint64(byteCount)
	total.frames++
	if !entry.eventEmitted && l.emittedEvents < roomAudioIngressMaxFirstEvents {
		entry.eventEmitted = true
		l.entries[key] = entry
		l.emittedEvents++
		fields := l.baseFields()
		fields[SessionDiagnosticFieldSourcePeer] = sourcePeer
		fields[SessionDiagnosticFieldSourcePeerID] = sourcePeer
		fields[SessionDiagnosticFieldDisposition] = string(disposition)
		fields[SessionDiagnosticFieldReason] = reason
		fields[SessionDiagnosticFieldByteCount] = strconv.Itoa(byteCount)
		fields[SessionDiagnosticFieldFrameCount] = "1"
		fields[SessionDiagnosticFieldCumulativeBytes] = strconv.FormatUint(entry.bytes, 10)
		fields[SessionDiagnosticFieldCumulativeFrames] = strconv.FormatUint(entry.frames, 10)
		firstRecord = &SessionDiagnosticRecord{Event: SessionDiagnosticEventRoomAudioIngress, Fields: fields}
	}
	l.mu.Unlock()
	if firstRecord != nil {
		l.emit(*firstRecord)
	}
}

func (l *roomAudioIngressLedger) finish() {
	if l == nil {
		return
	}
	l.finishOnce.Do(func() {
		// Accepted chunks that never reached a mixed output frame were stranded
		// in the participant mixer when its work stopped. Preserve their peer
		// attribution as terminal loss instead of silently dropping them.
		l.rejectPending(roomAudioIngressReasonParticipantTerminated)
		l.mu.Lock()
		sources := make(map[string]struct{}, len(l.entries))
		for key := range l.entries {
			sources[key.sourcePeer] = struct{}{}
		}
		sourceIDs := make([]string, 0, len(sources))
		for source := range sources {
			sourceIDs = append(sourceIDs, source)
		}
		sort.Strings(sourceIDs)
		totals := l.totals
		l.mu.Unlock()

		fields := l.baseFields()
		fields[SessionDiagnosticFieldDisposition] = "summary"
		fields[SessionDiagnosticFieldReason] = roomAudioIngressReasonParticipantTerminated
		fields[SessionDiagnosticFieldSourcePeer] = roomAudioIngressNoPeer
		if len(sourceIDs) > 0 {
			fields[SessionDiagnosticFieldSourcePeer] = strings.Join(sourceIDs, ",")
			fields[SessionDiagnosticFieldSourcePeers] = strings.Join(sourceIDs, ",")
		} else {
			fields[SessionDiagnosticFieldReason] = roomAudioIngressReasonNoContentfulPeerAudio
			fields[SessionDiagnosticFieldSourcePeers] = roomAudioIngressNoPeer
		}
		fields[SessionDiagnosticFieldSourcePeerID] = fields[SessionDiagnosticFieldSourcePeer]
		fields[SessionDiagnosticFieldByteCount] = strconv.FormatUint(totals.contentful.bytes, 10)
		fields[SessionDiagnosticFieldFrameCount] = strconv.FormatUint(totals.contentful.frames, 10)
		fields[SessionDiagnosticFieldContentfulBytes] = strconv.FormatUint(totals.contentful.bytes, 10)
		fields[SessionDiagnosticFieldContentfulFrames] = strconv.FormatUint(totals.contentful.frames, 10)
		fields[SessionDiagnosticFieldAcceptedBytes] = strconv.FormatUint(totals.delivered.bytes+totals.backpressured.bytes, 10)
		fields[SessionDiagnosticFieldAcceptedFrames] = strconv.FormatUint(totals.delivered.frames+totals.backpressured.frames, 10)
		fields[SessionDiagnosticFieldDeliveredBytes] = strconv.FormatUint(totals.delivered.bytes, 10)
		fields[SessionDiagnosticFieldDeliveredFrames] = strconv.FormatUint(totals.delivered.frames, 10)
		fields[SessionDiagnosticFieldBackpressuredBytes] = strconv.FormatUint(totals.backpressured.bytes, 10)
		fields[SessionDiagnosticFieldBackpressuredFrames] = strconv.FormatUint(totals.backpressured.frames, 10)
		fields[SessionDiagnosticFieldRejectedBytes] = strconv.FormatUint(totals.rejected.bytes, 10)
		fields[SessionDiagnosticFieldRejectedFrames] = strconv.FormatUint(totals.rejected.frames, 10)
		fields[SessionDiagnosticFieldContentLoss] = strconv.FormatBool(totals.rejected.frames > 0)
		l.emit(SessionDiagnosticRecord{Event: SessionDiagnosticEventRoomAudioIngressSummary, Fields: fields})
	})
}

func (l *roomAudioIngressLedger) baseFields() map[string]string {
	if l == nil {
		return nil
	}
	return map[string]string{
		SessionDiagnosticFieldRoomID:        l.roomID,
		SessionDiagnosticFieldParticipantID: l.participantID,
	}
}

func (l *roomAudioIngressLedger) emit(record SessionDiagnosticRecord) {
	if l == nil || l.sink == nil {
		return
	}
	l.sink.RecordSessionDiagnostic(record)
}

func roomPCMContentful(pcm []byte) bool {
	for _, value := range pcm {
		if value != 0 {
			return true
		}
	}
	return false
}

func roomAudioIngressDisposition(writeDisposition room.PCM16WriteDisposition, writeErr error) (RoomAudioIngressDisposition, string) {
	if writeErr != nil {
		return roomAudioIngressDispositionRejected, roomAudioIngressRejectionReason(writeErr)
	}
	if writeDisposition == room.PCM16WriteBackpressured {
		return roomAudioIngressDispositionBackpressured, "mixer_admitted_after_backpressure"
	}
	return roomAudioIngressDispositionDelivered, "mixer_admitted"
}

func roomAudioIngressRejectionReason(err error) string {
	switch {
	case errors.Is(err, room.ErrMixerClosed):
		return "mixer_closed"
	case errors.Is(err, room.ErrMixerInputMissing):
		return "mixer_input_missing"
	case errors.Is(err, room.ErrMixerInputBufferFull):
		return "mixer_input_queue_full"
	case errors.Is(err, room.ErrMixerInvalidFormat):
		return "invalid_pcm16"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context_deadline_exceeded"
	case errors.Is(err, room.ErrMixerOutputBackpressure):
		return "mixer_output_backpressure"
	default:
		return "mixer_rejected"
	}
}

func routeRoomPeerPCM(ctx context.Context, sourceID string, target *roomParticipantRuntime, pcm []byte) error {
	if target == nil || target.mixer == nil {
		return room.ErrMixerClosed
	}
	writeDisposition, writeErr := target.mixer.WriteContextWithDispositionAndObserver(ctx, sourceID, pcm, func(admitted room.PCM16WriteDisposition) {
		if target.ingress != nil {
			disposition, reason := roomAudioIngressDisposition(admitted, nil)
			target.ingress.admit(sourceID, disposition, reason, len(pcm), roomPCMContentful(pcm))
		}
	})
	if roomPCMContentful(pcm) && writeErr != nil && target.ingress != nil {
		disposition, reason := roomAudioIngressDisposition(writeDisposition, writeErr)
		target.ingress.record(sourceID, disposition, reason, len(pcm))
	}
	return writeErr
}

func evidenceParticipant(evidence *roomEvidence, participantID string) *roomParticipantEvidence {
	if evidence == nil {
		return nil
	}
	return evidence.participant(participantID)
}

func roomParticipantStreamSink(stream *RoomEventBroker, participantID string) RoomParticipantEventSink {
	if stream == nil {
		return RoomParticipantEventSink{}
	}
	return stream.ParticipantSink(participantID)
}
