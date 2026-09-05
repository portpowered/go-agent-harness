// Package probes defines service seams used by offline probe transports.
// Concrete session replay and metrics observation remain owned by the
// services composition root.
package probes

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

// MetricsSeries is the transport-neutral result of one direction/modality
// reconciliation series. It aliases the probe runner's value type so command
// rendering does not need a conversion at the service boundary.
type MetricsSeries = probe.MetricsSeries

// MetricsCollector replays one recorded session through the production
// runtime with an injected metrics recorder and independently checks the raw
// provider wire deltas. Implementations must not construct live devices,
// network transports, or executable tools.
type MetricsCollector interface {
	Collect(context.Context, string, string) ([]MetricsSeries, error)
}
