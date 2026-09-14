package main

import (
	"bytes"
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiooutput"
	audiooutputwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiooutput/wire"
	goaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func run() error {
	var pcm bytes.Buffer
	output, err := audiooutputwire.NewService().Open(audiooutput.Config{
		Path:       "-",
		Writer:     &pcm,
		SampleRate: goaudio.SampleRate,
	})
	if err != nil {
		return fmt.Errorf("open audio output: %w", err)
	}
	defer func() { _ = output.Close() }()
	want := []byte{0x01, 0x00, 0xfe, 0xff}
	if err := output.WriteDelta(context.Background(), want, messages.StreamMessage{Type: messages.StreamTypeAudioDelta}); err != nil {
		return fmt.Errorf("write audio delta: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close audio output: %w", err)
	}
	if !bytes.Equal(pcm.Bytes(), want) {
		return fmt.Errorf("PCM output = %x, want %x", pcm.Bytes(), want)
	}
	return nil
}
