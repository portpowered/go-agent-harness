package main

import "testing"

func TestExternalConsumerUsesPublicSessionTurnsContract(t *testing.T) {
	value, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if value.Schema != "audio-runtime.c87.public-session-turns/v1" || value.ConstructedVia != "sessionturns/wire.NewService" {
		t.Fatalf("identity = %+v", value)
	}
	if value.Connections != 1 || value.History != 2 || value.SecondConnections != 1 || value.SecondHistory != 1 || value.NextIndex != 3 || !value.AudioCopied || !value.SnapshotCopied || !value.InvalidPreserved || !value.Isolated || !value.Closed || len(value.SentTypes) != 3 || value.SentTypes[0] != "TEXT.DELTA" || value.SentTypes[1] != "AUDIO.DELTA" || value.SentTypes[2] != "MESSAGE.END" {
		t.Fatalf("contract result = %+v", value)
	}
}
