package service

func parseRoomReplayParticipantArtifactFields(object roomReplayJSONObject, field string) (map[string]roomReplayArtifactRef, error) {
	artifacts := make(map[string]roomReplayArtifactRef)
	if raw, ok := roomReplayRawField(object, "artifacts"); ok {
		entries, err := parseRoomReplayParticipantArtifacts(raw, field+".artifacts")
		if err != nil {
			return nil, err
		}
		if err := mergeRoomReplayParticipantArtifacts(artifacts, entries, field); err != nil {
			return nil, err
		}
	}
	if err := parseRoomReplayDirectArtifactFields(artifacts, object, field); err != nil {
		return nil, err
	}
	return artifacts, nil
}

func mergeRoomReplayParticipantArtifacts(destination, entries map[string]roomReplayArtifactRef, field string) error {
	for role, artifact := range entries {
		if _, exists := destination[role]; exists {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, field+".artifacts", artifact.Path, "one reference for role", role, ErrInvalidRoomReplayBundle)
		}
		destination[role] = artifact
	}
	return nil
}

func parseRoomReplayDirectArtifactFields(artifacts map[string]roomReplayArtifactRef, object roomReplayJSONObject, field string) error {
	for _, direct := range []struct {
		role string
		keys []string
	}{
		{roomReplayArtifactRoleCapture, []string{"capture", "capture_path", "session_capture", "session_capture_path", "replay_capture", "provider_capture", "provider_capture_path"}},
		{roomReplayArtifactRoleSentPCM, []string{"sent_pcm", "sent_pcm_path", "sent"}},
		{roomReplayArtifactRoleReceivedPCM, []string{"received_pcm", "received_pcm_path", "received"}},
		{roomReplayArtifactRoleEvents, []string{"events", "events_path", "participant_events"}},
		{roomReplayArtifactRoleWAV, []string{"wav", "wav_path", "legacy_wav", "audio_wav"}},
		{roomReplayArtifactRoleDiagnostics, []string{"diagnostics", "diagnostics_path", "diagnostic"}},
		{roomReplayArtifactRoleDeltas, []string{"deltas", "deltas_path", "delta"}},
	} {
		if _, exists := artifacts[direct.role]; exists {
			continue
		}
		if raw, ok := roomReplayRawField(object, direct.keys...); ok {
			artifact, err := parseRoomReplayArtifactRef(raw, field+"."+direct.role, direct.role)
			if err != nil {
				return err
			}
			artifacts[direct.role] = artifact
		}
	}
	return nil
}
