# Changelog

## Unreleased

### Removed

Redundant alias forwarders were deleted. Each one has a direct replacement
that behaves identically:

| Removed | Use instead |
| --- | --- |
| `probe.Aggregate` | `probe.AggregateScenarioResults` |
| `probe.ValidateCatalog` | `probe.ValidateGoalCatalog` |
| `transcript.(*AgentCapture).ProviderIngress` | `(*AgentCapture).Inbound` |
| `transcript.(*AgentCapture).ProviderEgress` | `(*AgentCapture).Outbound` |
| `transcript.NewClient` | `transcript.NewClientCapture` |
| `transcript.NewTranscriptWriter` | `transcript.NewWriter` |
| `transcript.NewTransparentTee` | `transcript.NewTee` |
| `transcript.WithMaxSegmentBytes` | `transcript.WithSegmentSize` |
| `transcript.WithReporter` | `transcript.WithDegradationReporter` |

The participants session admission hook was removed. The model runner no
longer checks whether a `messages.Session` implements
`SessionAdmissionClosed() bool` or
`SessionAdmissionAllows(messages.StreamMessage) bool`. No session in this
repository implements them any more. A session that has to refuse input after
shutdown should reject it from `SendWithOutcome` and return
`messages.SessionSendClosed`.
