package main

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate"
	audioratewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func main() {
	service := audioratewire.NewService()
	pcm := codec.EncodePCM16([]int16{1, 2, 3})
	converted, err := service.ConvertPCM(context.Background(), pcm, wavio.Rate16kHz, wavio.Rate24kHz)
	if err != nil {
		panic(err)
	}
	rate, err := service.ResolveSampleRate(context.Background(), audiorate.RateResolutionRequest{Provider: audiorate.ProviderOpenAI})
	if err != nil {
		panic(err)
	}
	fmt.Printf("samples=%d rate=%d\n", len(converted)/2, rate)
}
