# Changelog

## Unreleased

### Removed

Redundant alias forwarders and re-exports were deleted. Each one has a direct
replacement that behaves identically:

| Removed | Use instead |
| --- | --- |
| `room.MeasurePCM16Correlation` | `room.NormalizedPCM16CrossCorrelation` |
| `stream.Analyze` | `stream.AnalyzePCM16` |
| `stream.ValidatePCM16` | `stream.AssertPCM16` |
| `selfhearing.NewPCM16SelfHearingController` | `selfhearing.NewPCM16SelfHearingDetector` |
| `selfhearing.NewPCM16SelfHearingControllerForTopology` | `selfhearing.NewPCM16SelfHearingDetectorForTopology` |
| `room.ErrInvalidPCM16AnalysisInput`, `room.InvalidPCM16AnalysisInputError`, `room.PCM16AnalysisFrameDuration`, `room.PCM16AnalysisSilenceFloorDBFS` | `stream.*` (same names) |
| `selfhearing.PCM16MediaFrame`, `selfhearing.PCM16CorrelationMeasurement` | `stream.*` (same names) |
| `wavio.Decode` | `wavio.Read` |
| `wavio.Encode` | `wavio.Write` |
