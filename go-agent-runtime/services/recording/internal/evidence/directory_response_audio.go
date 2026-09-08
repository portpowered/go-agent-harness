package evidence

import "slices"

// Media and normalized messages have independent consumers. Join their summary
// by provider response identity at finalization, never by goroutine arrival order.
// Raw artifacts retain their actual admission order and exact PCM offsets.
type evidenceResponseAudio struct {
	bytes, offset uint64
	segment       string
}

func (c *evidenceConversation) trackResponse(id string) {
	if id != "" && !slices.Contains(c.turn.responseIDs, id) {
		c.turn.responseIDs = append(c.turn.responseIDs, id)
	}
}

func (c *evidenceConversation) recordResponseAudio(id string, count, offset uint64, segment string) {
	if c.responseAudio == nil {
		c.responseAudio = make(map[string]evidenceResponseAudio)
	}
	audio := c.responseAudio[id]
	if audio.bytes == 0 {
		audio.offset = offset
		audio.segment = segment
	}
	audio.bytes += count
	c.responseAudio[id] = audio
}

func (c evidenceConversation) withResponseAudio(turn evidenceTurn) evidenceTurn {
	for _, id := range turn.responseIDs {
		audio := c.responseAudio[id]
		if audio.bytes == 0 {
			continue
		}
		if turn.outputAudio == 0 || audio.offset < turn.outputOffset {
			turn.outputOffset = audio.offset
		}
		turn.outputAudio += audio.bytes
		if !slices.Contains(turn.outputSegments, audio.segment) {
			turn.outputSegments = append(turn.outputSegments, audio.segment)
		}
	}
	return turn
}
