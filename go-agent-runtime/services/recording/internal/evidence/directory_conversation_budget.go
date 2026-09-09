package evidence

import (
	"errors"
	"io"
)

// The conversation log is a convenience projection. Raw transcript, PCM and
// provider artifacts remain authoritative and are deliberately not charged to
// this budget. The byte limit is a conservative upper bound for the retained
// projection and its JSON representation: strings are charged at the maximum
// expansion used by encoding/json, while fixed charges cover headers, map
// entries and slice capacity. This keeps finalization bounded without making
// the public recording contract depend on the caller's input lifetime.
const (
	directorySummaryMaxBytes int64 = 2 << 20
	directorySummaryMaxItems       = 4096

	// encoding/json can emit a six-byte \\u00xx escape for one input byte. The
	// fixed charge also covers a retained string header and JSON punctuation.
	summaryJSONEscapeMultiplier int64 = 6
	summaryStringFixedBytes     int64 = 32
	summarySliceEntryBytes      int64 = 64
	summaryMapEntryBytes        int64 = 128
	summaryTurnFixedBytes       int64 = 768
	summaryToolEventFixedBytes  int64 = 256

	// A final JSON line is built before it is appended to the bounded output.
	// Keep a fixed per-turn allowance for field names, numeric fields and
	// encoding scratch in addition to the retained value charges.
	summaryJSONTurnFixedBytes int64 = 512
)

var (
	errConversationSummaryBudget = errors.New("recording conversation summary budget exceeded")
	errConversationSummaryItems  = errors.New("recording conversation summary item limit exceeded")
)

type summaryBudget struct {
	bytes int64
	items int
}

func (b *summaryBudget) reserve(bytes int64, items int) bool {
	if b == nil || bytes < 0 || items < 0 {
		return false
	}
	if bytes > directorySummaryMaxBytes-b.bytes || items > directorySummaryMaxItems-b.items {
		return false
	}
	b.bytes += bytes
	b.items += items
	return true
}

func (b *summaryBudget) release(bytes int64, items int) {
	if b == nil {
		return
	}
	b.bytes -= bytes
	b.items -= items
	if b.bytes < 0 {
		b.bytes = 0
	}
	if b.items < 0 {
		b.items = 0
	}
}

func summaryStringBytes(value string) int64 {
	length := int64(len(value))
	if length > (directorySummaryMaxBytes-summaryStringFixedBytes)/summaryJSONEscapeMultiplier {
		return directorySummaryMaxBytes + 1
	}
	return summaryStringFixedBytes + length*summaryJSONEscapeMultiplier
}

func summaryBudgetError(items bool) error {
	if items {
		return errors.Join(errConversationSummaryBudget, errConversationSummaryItems, io.ErrShortBuffer)
	}
	return errors.Join(errConversationSummaryBudget, io.ErrShortBuffer)
}

func summaryCost(values ...string) int64 {
	cost := int64(0)
	for _, value := range values {
		valueCost := summaryStringBytes(value)
		if valueCost > directorySummaryMaxBytes-cost {
			return directorySummaryMaxBytes + 1
		}
		cost += valueCost
	}
	return cost
}
