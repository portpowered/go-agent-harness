package rooms

import "time"

const (
	// RoomLatencyArtifactPath is the stable room-level timing ledger.
	RoomLatencyArtifactPath = "room-latency.json"
	// RoomLatencyBundleSchemaVersion is incremented when ledger semantics
	// change. Reports are derived from this ledger rather than live state.
	RoomLatencyBundleSchemaVersion = 1

	RoomLatencyEventSpeakerPCM     RoomLatencyEventKind = "speaker_pcm_segment"
	RoomLatencyEventEndOfSpeech    RoomLatencyEventKind = "end_of_speech"
	RoomLatencyEventInputCommit    RoomLatencyEventKind = "input_commit"
	RoomLatencyEventResponseCreate RoomLatencyEventKind = "response_create"
	RoomLatencyEventProviderAudio  RoomLatencyEventKind = "provider_audio_delta"
	RoomLatencyEventPeerAudio      RoomLatencyEventKind = "peer_audio_emission"

	RoomLatencyReasonMissingSpeakerSample    = "missing_speaker_sample"
	RoomLatencyReasonMissingEndOfSpeech      = "missing_end_of_speech"
	RoomLatencyReasonMissingInputCommit      = "missing_input_commit"
	RoomLatencyReasonMissingResponseCreate   = "missing_response_create"
	RoomLatencyReasonMissingProviderAudio    = "missing_provider_audio"
	RoomLatencyReasonMissingPeerAudio        = "missing_peer_audio"
	RoomLatencyReasonDuplicateSpeakerSample  = "duplicate_speaker_sample"
	RoomLatencyReasonDuplicateEndOfSpeech    = "duplicate_end_of_speech"
	RoomLatencyReasonDuplicateInputCommit    = "duplicate_input_commit"
	RoomLatencyReasonDuplicateResponseCreate = "duplicate_response_create"
	RoomLatencyReasonDuplicateProviderAudio  = "duplicate_provider_audio"
	RoomLatencyReasonDuplicatePeerAudio      = "duplicate_peer_audio"
	RoomLatencyReasonUncorrelatedLandmarks   = "uncorrelated_landmarks"
	RoomLatencyReasonReorderedLandmarks      = "reordered_landmarks"
	RoomLatencyReasonInvalidSpeakerSample    = "invalid_speaker_sample"
	RoomLatencyReasonInvalidTimestamp        = "invalid_timestamp"
	RoomLatencyReasonGapOutsideTolerance     = "gap_outside_tolerance"
)

// RoomLatencyEventKind identifies one authoritative room timing boundary.
type RoomLatencyEventKind string

// RoomLatencyPCMFormat is the audio metadata needed to place the final
// sample of a recorded segment on the shared clock.
type RoomLatencyPCMFormat struct {
	SampleRateHz    int   `json:"sample_rate_hz"`
	Channels        int   `json:"channels"`
	FrameDurationNS int64 `json:"frame_duration_ns"`
}

// RoomLatencyEvent is one ordered, clock-stamped timing observation.
type RoomLatencyEvent struct {
	Sequence          uint64               `json:"sequence"`
	Kind              RoomLatencyEventKind `json:"kind"`
	TransitionID      string               `json:"transition_id,omitempty"`
	ParticipantID     string               `json:"participant_id,omitempty"`
	PeerParticipantID string               `json:"peer_participant_id,omitempty"`
	TurnIndex         int                  `json:"turn_index,omitempty"`
	ResponseID        string               `json:"response_id,omitempty"`
	Tick              uint64               `json:"tick"`
	Timestamp         time.Time            `json:"timestamp"`
	PCMBytes          int                  `json:"pcm_bytes,omitempty"`
	SampleRateHz      int                  `json:"sample_rate_hz,omitempty"`
	Channels          int                  `json:"channels,omitempty"`
}

// RoomLatencyBundle is the durable source-of-truth timing ledger.
type RoomLatencyBundle struct {
	SchemaVersion int                  `json:"schema_version"`
	Format        RoomLatencyPCMFormat `json:"format"`
	Events        []RoomLatencyEvent   `json:"events"`
}

// RoomLatencyLandmark is a normalized boundary used by a derived report.
type RoomLatencyLandmark struct {
	Sequence     uint64    `json:"sequence"`
	Tick         uint64    `json:"tick"`
	Timestamp    time.Time `json:"timestamp"`
	PCMBytes     int       `json:"pcm_bytes,omitempty"`
	SampleRateHz int       `json:"sample_rate_hz,omitempty"`
	Channels     int       `json:"channels,omitempty"`
}

// RoomLatencyTransition contains correlated landmarks and durations for one
// peer response.
type RoomLatencyTransition struct {
	TransitionID      string `json:"transition_id"`
	ParticipantID     string `json:"participant_id"`
	PeerParticipantID string `json:"peer_participant_id,omitempty"`
	TurnIndex         int    `json:"turn_index,omitempty"`
	ResponseID        string `json:"response_id,omitempty"`
	Eligible          bool   `json:"eligible"`
	ExclusionReason   string `json:"exclusion_reason,omitempty"`

	LastSpeakerSample  *RoomLatencyLandmark `json:"last_speaker_sample,omitempty"`
	EndOfSpeech        *RoomLatencyLandmark `json:"end_of_speech,omitempty"`
	InputCommit        *RoomLatencyLandmark `json:"input_commit,omitempty"`
	ResponseCreate     *RoomLatencyLandmark `json:"response_create,omitempty"`
	FirstProviderAudio *RoomLatencyLandmark `json:"first_provider_audio,omitempty"`
	FirstPeerAudio     *RoomLatencyLandmark `json:"first_peer_audio,omitempty"`

	DetectionMS           int64 `json:"detection_ms"`
	CommitAfterEndMS      int64 `json:"commit_after_end_ms"`
	ResponseAfterCommitMS int64 `json:"response_after_commit_ms"`
	DispatchMS            int64 `json:"dispatch_ms"`
	ProviderMS            int64 `json:"provider_ms"`
	LocalOutputMS         int64 `json:"local_output_ms"`
	HarnessOwnedMS        int64 `json:"harness_owned_ms"`
	FourBucketSumMS       int64 `json:"four_bucket_sum_ms"`
	DirectGapMS           int64 `json:"direct_gap_ms"`
	TotalMS               int64 `json:"total_ms"`
}

type RoomLatencyExclusion struct {
	TransitionID  string `json:"transition_id,omitempty"`
	ParticipantID string `json:"participant_id,omitempty"`
	Reason        string `json:"reason"`
	EventCount    int    `json:"event_count,omitempty"`
}

type RoomLatencyStatistics struct {
	SampleCount int   `json:"sample_count"`
	MedianMS    int64 `json:"median_ms"`
	P95MS       int64 `json:"p95_ms"`
	MaxMS       int64 `json:"max_ms"`
}

type RoomLatencySummary struct {
	Detection    RoomLatencyStatistics `json:"detection"`
	Dispatch     RoomLatencyStatistics `json:"dispatch"`
	Provider     RoomLatencyStatistics `json:"provider"`
	LocalOutput  RoomLatencyStatistics `json:"local_output"`
	HarnessOwned RoomLatencyStatistics `json:"harness_owned"`
	Total        RoomLatencyStatistics `json:"total"`
}

// RoomLatencyReport is derived exclusively from a finalized ledger.
type RoomLatencyReport struct {
	EligibleCount int                     `json:"eligible_count"`
	ExcludedCount int                     `json:"excluded_count"`
	Transitions   []RoomLatencyTransition `json:"transitions"`
	Exclusions    []RoomLatencyExclusion  `json:"exclusions,omitempty"`
	Summary       RoomLatencySummary      `json:"summary"`
}
