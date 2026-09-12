package service

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog"

func cloneTurn(source *turn) turn {
	copyTurn := turn{
		inputText:                source.inputText,
		inputFullText:            source.inputFullText,
		inputTranscriptCompleted: source.inputTranscriptCompleted,
		inputAudioBytes:          source.inputAudioBytes,
		inputCommitted:           source.inputCommitted,
		inputOrdinal:             source.inputOrdinal,
		inputOrdinalSet:          source.inputOrdinalSet,
		committedAt:              source.committedAt,
		firstResponseAudioAt:     source.firstResponseAudioAt,
		inputSegments:            cloneStrings(source.inputSegments),
		responseFullText:         source.responseFullText,
		outputAudioBytes:         source.outputAudioBytes,
		outputSegments:           cloneStrings(source.outputSegments),
		complete:                 source.complete,
		toolEvents:               cloneToolEvents(source.toolEvents),
		toolCallMessage:          source.toolCallMessage,
	}
	copyTurn.inputTranscript.WriteString(source.inputTranscript.String())
	copyTurn.responseDeltas.WriteString(source.responseDeltas.String())
	return copyTurn
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneImage(image *conversationlog.ImageEvidence) *conversationlog.ImageEvidence {
	if image == nil {
		return nil
	}
	copyImage := *image
	return &copyImage
}

func cloneToolEvents(events []conversationlog.ToolEvent) []conversationlog.ToolEvent {
	if len(events) == 0 {
		return nil
	}
	cloned := make([]conversationlog.ToolEvent, len(events))
	for index, event := range events {
		cloned[index] = event
		cloned[index].Image = cloneImage(event.Image)
	}
	return cloned
}
