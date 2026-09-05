package rooms

import "encoding/json"

// ReportingService derives the versioned latency report from a finalized
// evidence bundle. The command renders the returned JSON document.
type ReportingService interface {
	LatencyReport(string) (json.RawMessage, error)
}
