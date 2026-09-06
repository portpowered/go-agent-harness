package latency

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"

type RoomLatencyEventKind = rooms.RoomLatencyEventKind
type RoomLatencyPCMFormat = rooms.RoomLatencyPCMFormat
type RoomLatencyEvent = rooms.RoomLatencyEvent
type RoomLatencyBundle = rooms.RoomLatencyBundle
type RoomLatencyLandmark = rooms.RoomLatencyLandmark
type RoomLatencyTransition = rooms.RoomLatencyTransition
type RoomLatencyExclusion = rooms.RoomLatencyExclusion
type RoomLatencyStatistics = rooms.RoomLatencyStatistics
type RoomLatencySummary = rooms.RoomLatencySummary
type RoomLatencyReport = rooms.RoomLatencyReport

const (
	RoomLatencyBundleSchemaVersion           = rooms.RoomLatencyBundleSchemaVersion
	RoomLatencyEventSpeakerPCM               = rooms.RoomLatencyEventSpeakerPCM
	RoomLatencyEventEndOfSpeech              = rooms.RoomLatencyEventEndOfSpeech
	RoomLatencyEventInputCommit              = rooms.RoomLatencyEventInputCommit
	RoomLatencyEventResponseCreate           = rooms.RoomLatencyEventResponseCreate
	RoomLatencyEventProviderAudio            = rooms.RoomLatencyEventProviderAudio
	RoomLatencyEventPeerAudio                = rooms.RoomLatencyEventPeerAudio
	RoomLatencyReasonMissingSpeakerSample    = rooms.RoomLatencyReasonMissingSpeakerSample
	RoomLatencyReasonMissingEndOfSpeech      = rooms.RoomLatencyReasonMissingEndOfSpeech
	RoomLatencyReasonMissingInputCommit      = rooms.RoomLatencyReasonMissingInputCommit
	RoomLatencyReasonMissingResponseCreate   = rooms.RoomLatencyReasonMissingResponseCreate
	RoomLatencyReasonMissingProviderAudio    = rooms.RoomLatencyReasonMissingProviderAudio
	RoomLatencyReasonMissingPeerAudio        = rooms.RoomLatencyReasonMissingPeerAudio
	RoomLatencyReasonDuplicateSpeakerSample  = rooms.RoomLatencyReasonDuplicateSpeakerSample
	RoomLatencyReasonDuplicateEndOfSpeech    = rooms.RoomLatencyReasonDuplicateEndOfSpeech
	RoomLatencyReasonDuplicateInputCommit    = rooms.RoomLatencyReasonDuplicateInputCommit
	RoomLatencyReasonDuplicateResponseCreate = rooms.RoomLatencyReasonDuplicateResponseCreate
	RoomLatencyReasonDuplicateProviderAudio  = rooms.RoomLatencyReasonDuplicateProviderAudio
	RoomLatencyReasonDuplicatePeerAudio      = rooms.RoomLatencyReasonDuplicatePeerAudio
	RoomLatencyReasonUncorrelatedLandmarks   = rooms.RoomLatencyReasonUncorrelatedLandmarks
	RoomLatencyReasonReorderedLandmarks      = rooms.RoomLatencyReasonReorderedLandmarks
	RoomLatencyReasonInvalidSpeakerSample    = rooms.RoomLatencyReasonInvalidSpeakerSample
	RoomLatencyReasonInvalidTimestamp        = rooms.RoomLatencyReasonInvalidTimestamp
	RoomLatencyReasonGapOutsideTolerance     = rooms.RoomLatencyReasonGapOutsideTolerance
)

type ObservationKind = rooms.LatencyObservationKind

const (
	ObservationInputCommit    ObservationKind = rooms.LatencyObservationInputCommit
	ObservationResponseCreate ObservationKind = rooms.LatencyObservationResponseCreate
)

type Observation = rooms.LatencyObservation
