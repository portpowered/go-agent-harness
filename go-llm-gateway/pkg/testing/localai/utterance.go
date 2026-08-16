package localai

import (
	_ "embed"
	"encoding/base64"
	"fmt"
)

// deterministicPCM16Fixture is a checked-in, mono 16 kHz PCM16 utterance.
// It is decoded locally so the live test never invokes a TTS binary.
//
//go:embed testdata/utterance.pcm.b64
var deterministicPCM16Fixture string

func deterministicPCM16Utterance() []byte {
	audio, err := base64.StdEncoding.DecodeString(deterministicPCM16Fixture)
	if err != nil {
		panic(fmt.Sprintf("decode checked-in PCM16 fixture: %v", err))
	}
	return audio
}
