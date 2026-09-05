package audio

type CaptureQueueStats struct {
	QueuedSamples    int
	HighWaterSamples int
	CapturedSamples  uint64
	CompletedFrames  uint64
	DroppedFrames    uint64
	DroppedSamples   uint64
	DropPolicy       string
	SequenceGaps     uint64
}
