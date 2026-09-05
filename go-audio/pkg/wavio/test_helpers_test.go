package wavio

import "encoding/binary"

type testChunk struct {
	id   string
	data []byte
}

func makeChunk(id string, data []byte) testChunk {
	return testChunk{id: id, data: data}
}

func buildWAV(chunks ...testChunk) []byte {
	riffSize := 4
	for _, chunk := range chunks {
		riffSize += 8 + len(chunk.data) + len(chunk.data)%2
	}

	encoded := make([]byte, 8+riffSize)
	copy(encoded[0:4], "RIFF")
	binary.LittleEndian.PutUint32(encoded[4:8], uint32(riffSize))
	copy(encoded[8:12], "WAVE")
	offset := 12
	for _, chunk := range chunks {
		copy(encoded[offset:offset+4], chunk.id)
		binary.LittleEndian.PutUint32(encoded[offset+4:offset+8], uint32(len(chunk.data)))
		offset += 8
		copy(encoded[offset:offset+len(chunk.data)], chunk.data)
		offset += len(chunk.data)
		if len(chunk.data)%2 == 1 {
			offset++
		}
	}
	return encoded
}

func pcmFormatPayload(rate uint32) []byte {
	payload := make([]byte, 16)
	binary.LittleEndian.PutUint16(payload[0:2], pcmFormat)
	binary.LittleEndian.PutUint16(payload[2:4], monoChannels)
	binary.LittleEndian.PutUint32(payload[4:8], rate)
	binary.LittleEndian.PutUint32(payload[8:12], rate*pcm16BlockAlign)
	binary.LittleEndian.PutUint16(payload[12:14], pcm16BlockAlign)
	binary.LittleEndian.PutUint16(payload[14:16], pcm16Bits)
	return payload
}

func pcmSamples(samples []int16) []byte {
	payload := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(payload[index*2:], uint16(sample))
	}
	return payload
}

func canonicalWAV(rate int, samples []int16) []byte {
	return buildWAV(
		makeChunk("fmt ", pcmFormatPayload(uint32(rate))),
		makeChunk("data", pcmSamples(samples)),
	)
}
