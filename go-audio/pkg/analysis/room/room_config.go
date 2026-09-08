package room

func applyPCM16RoomAnalysisDefaults(config PCM16RoomAnalysisConfig) PCM16RoomAnalysisConfig {
	defaults := DefaultPCM16RoomAnalysisConfig()
	if config.CorrelationLagWindow.Min == 0 && config.CorrelationLagWindow.Max == 0 {
		config.CorrelationLagWindow = defaults.CorrelationLagWindow
	}
	if config.CorrelationSilenceFloorDBFS == 0 {
		config.CorrelationSilenceFloorDBFS = defaults.CorrelationSilenceFloorDBFS
	}
	if config.MinPeerCorrelation == 0 {
		config.MinPeerCorrelation = defaults.MinPeerCorrelation
	}
	if config.MaxSelfCorrelation == 0 {
		config.MaxSelfCorrelation = defaults.MaxSelfCorrelation
	}
	if config.BargeInSpeechThresholdDBFS == 0 {
		config.BargeInSpeechThresholdDBFS = defaults.BargeInSpeechThresholdDBFS
	}
	if config.MaxBargeInLatency == 0 {
		config.MaxBargeInLatency = defaults.MaxBargeInLatency
	}
	if config.MaxLoudnessDifferenceDB == 0 {
		config.MaxLoudnessDifferenceDB = defaults.MaxLoudnessDifferenceDB
	}
	if config.MaxDriftAbsolute == 0 {
		config.MaxDriftAbsolute = defaults.MaxDriftAbsolute
	}
	if config.MaxDriftFraction == 0 {
		config.MaxDriftFraction = defaults.MaxDriftFraction
	}
	return config
}

func validatePCM16RoomAnalysisConfig(config PCM16RoomAnalysisConfig) error {
	if err := validateRoomCorrelationLag(config); err != nil {
		return err
	}
	if err := validateRoomCorrelationSilenceFloor(config); err != nil {
		return err
	}
	if err := validateRoomPeerCorrelation(config); err != nil {
		return err
	}
	if err := validateRoomSelfCorrelation(config); err != nil {
		return err
	}
	if err := validateRoomBargeThreshold(config); err != nil {
		return err
	}
	if err := validateRoomBargeLatency(config); err != nil {
		return err
	}
	if err := validateRoomLoudnessDifference(config); err != nil {
		return err
	}
	if err := validateRoomDriftAbsolute(config); err != nil {
		return err
	}
	if err := validateRoomDriftFraction(config); err != nil {
		return err
	}
	return nil
}

func validateRoomCorrelationLag(config PCM16RoomAnalysisConfig) error {
	if config.CorrelationLagWindow.Min > config.CorrelationLagWindow.Max {
		return invalidPCM16RoomAnalysis("correlation_lag_window", "min must be at or before max")
	}
	return nil
}

func validateRoomCorrelationSilenceFloor(config PCM16RoomAnalysisConfig) error {
	if !isFinite(config.CorrelationSilenceFloorDBFS) || config.CorrelationSilenceFloorDBFS > 0 {
		return invalidPCM16RoomAnalysis("correlation_silence_floor_dbfs", "must be finite and at or below 0")
	}
	return nil
}

func validateRoomPeerCorrelation(config PCM16RoomAnalysisConfig) error {
	if !isFinite(config.MinPeerCorrelation) || config.MinPeerCorrelation < 0 || config.MinPeerCorrelation > 1 {
		return invalidPCM16RoomAnalysis("min_peer_correlation", "must be between 0 and 1")
	}
	return nil
}

func validateRoomSelfCorrelation(config PCM16RoomAnalysisConfig) error {
	if !isFinite(config.MaxSelfCorrelation) || config.MaxSelfCorrelation < 0 || config.MaxSelfCorrelation > 1 {
		return invalidPCM16RoomAnalysis("max_self_correlation", "must be between 0 and 1")
	}
	return nil
}

func validateRoomBargeThreshold(config PCM16RoomAnalysisConfig) error {
	if !isFinite(config.BargeInSpeechThresholdDBFS) || config.BargeInSpeechThresholdDBFS > 0 {
		return invalidPCM16RoomAnalysis("barge_in_speech_threshold_dbfs", "must be finite and at or below 0")
	}
	return nil
}

func validateRoomBargeLatency(config PCM16RoomAnalysisConfig) error {
	if config.MaxBargeInLatency <= 0 {
		return invalidPCM16RoomAnalysis("max_barge_in_latency", "must be positive")
	}
	return nil
}

func validateRoomLoudnessDifference(config PCM16RoomAnalysisConfig) error {
	if !isFinite(config.MaxLoudnessDifferenceDB) || config.MaxLoudnessDifferenceDB < 0 {
		return invalidPCM16RoomAnalysis("max_loudness_difference_db", "must be finite and non-negative")
	}
	return nil
}

func validateRoomDriftAbsolute(config PCM16RoomAnalysisConfig) error {
	if config.MaxDriftAbsolute <= 0 {
		return invalidPCM16RoomAnalysis("max_drift_absolute", "must be positive")
	}
	return nil
}

func validateRoomDriftFraction(config PCM16RoomAnalysisConfig) error {
	if !isFinite(config.MaxDriftFraction) || config.MaxDriftFraction < 0 {
		return invalidPCM16RoomAnalysis("max_drift_fraction", "must be finite and non-negative")
	}
	return nil
}
