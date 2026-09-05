package audio

import "github.com/portpowered/go-agent-harness/go-audio/pkg/codec"

// PCM16Framer converts a continuous byte stream into provider PCM packets.
// It shares the stream Processor with device capture and playback. Flush emits
// the exact tail and resets filter history for the next utterance.
type PCM16Framer struct{ processor *Processor }

func NewPCM16Framer(sourceRate, destinationRate, quantum int) (*PCM16Framer, error) {
	processor, err := NewProcessor(PCM16DeviceFormat(sourceRate), PCM16DeviceFormat(destinationRate), quantum)
	if err != nil {
		return nil, err
	}
	return &PCM16Framer{processor: processor}, nil
}

func (f *PCM16Framer) Push(pcm []byte) ([][]byte, error) {
	samples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return nil, err
	}
	frames, err := f.processor.Process(PCMFrame{Samples: samples})
	return encodeFrames(frames), err
}

func (f *PCM16Framer) Flush() ([][]byte, error) {
	frames, err := f.processor.Process(PCMFrame{EndOfResponse: true})
	if err != nil {
		return nil, err
	}
	_, err = f.processor.Reset()
	return encodeFrames(frames), err
}

func encodeFrames(frames []PCMFrame) [][]byte {
	result := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		if len(frame.Samples) > 0 {
			result = append(result, codec.EncodePCM16(frame.Samples))
		}
	}
	return result
}
