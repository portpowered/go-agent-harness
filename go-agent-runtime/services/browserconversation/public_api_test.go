package browserconversation

import (
	"context"
	"testing"
)

func TestScheduledAudioInputsCloneCopiesThePCM(t *testing.T) {
	if ScheduledAudioInputs(nil).Clone() != nil {
		t.Fatal("Clone() of nil inputs is not nil")
	}
	original := ScheduledAudioInputs{{AfterCompletedTurns: 1, PCM: []byte{1, 2}, SourceSampleRate: 24000, EndOfTurn: true}}
	clone := original.Clone()
	clone[0].PCM[0] = 9
	if original[0].PCM[0] != 1 || clone[0].AfterCompletedTurns != 1 || !clone[0].EndOfTurn {
		t.Fatalf("Clone() = %#v shares PCM with or drops fields of %#v", clone, original)
	}
}

func TestBrowserConversationValidatorFuncAdaptsAFunction(t *testing.T) {
	var _ BrowserConversationValidator = BrowserConversationValidatorFunc(nil)
	verdict, err := BrowserConversationValidatorFunc(func(context.Context, BrowserConversationResult) (BrowserConversationValidatorVerdict, error) {
		return BrowserConversationValidatorVerdict{Passed: true, Summary: "ok"}, nil
	}).ValidateBrowserConversation(t.Context(), BrowserConversationResult{})
	if err != nil || !verdict.Passed || verdict.Summary != "ok" {
		t.Fatalf("ValidateBrowserConversation() = %#v, %v", verdict, err)
	}
	if _, err := BrowserConversationValidatorFunc(nil).ValidateBrowserConversation(t.Context(), BrowserConversationResult{}); err == nil {
		t.Fatal("nil validator func returned no error")
	}
}
