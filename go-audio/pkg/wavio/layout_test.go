package wavio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestInspectValidatesExtentWithoutReadingPayload(t *testing.T) {
	for _, rate := range SupportedSampleRates() {
		var wav bytes.Buffer
		if err := Write(&wav, rate, make([]int16, 48000)); err != nil {
			t.Fatal(err)
		}
		reader := &metadataReadCounter{ReadSeeker: bytes.NewReader(wav.Bytes())}
		layout, err := Inspect(reader)
		if err != nil {
			t.Fatal(err)
		}
		if reader.bytes != 44 || layout.SampleRate != rate || layout.DataBytes != 96000 || layout.DataOffset != 44 {
			t.Fatalf("layout=%+v metadata bytes=%d", layout, reader.bytes)
		}
		pos, _ := reader.Seek(0, io.SeekCurrent)
		if pos != layout.DataOffset {
			t.Fatalf("position=%d", pos)
		}
		_, err = Inspect(bytes.NewReader(wav.Bytes()[:wav.Len()-1]))
		if !errors.Is(err, ErrTruncated) {
			t.Fatalf("truncated extent accepted: %v", err)
		}
	}
}

type metadataReadCounter struct {
	io.ReadSeeker
	bytes int
}

func (r *metadataReadCounter) Read(p []byte) (int, error) {
	n, err := r.ReadSeeker.Read(p)
	r.bytes += n
	return n, err
}

func TestInspectRejectsChunkOutsideDeclaredRIFF(t *testing.T) {
	var wav bytes.Buffer
	if err := Write(&wav, Rate24kHz, []int16{1, 2}); err != nil {
		t.Fatal(err)
	}
	data := wav.Bytes()
	binary.LittleEndian.PutUint32(data[4:8], 36)
	if _, err := Inspect(bytes.NewReader(data)); !errors.Is(err, ErrTruncated) {
		t.Fatalf("chunk outside RIFF: %v", err)
	}
}

func TestPCM16ContainerRateIsIndependentOfResamplerAdmission(t *testing.T) {
	for _, rate := range []int{1000, 44100} {
		header, err := PCM16Header(rate, 4)
		if err != nil {
			t.Fatal(err)
		}
		encoded := append(header[:], 1, 0, 2, 0)
		layout, err := Inspect(bytes.NewReader(encoded))
		if err != nil || layout.SampleRate != rate || layout.DataBytes != 4 {
			t.Fatalf("valid container rate %d rejected: %+v %v", rate, layout, err)
		}
		if err := ValidateSampleRate(rate); err == nil {
			t.Fatalf("unsupported resampler rate %d admitted", rate)
		}
	}
	if _, err := PCM16Header(0, 0); err == nil {
		t.Fatal("zero rate accepted")
	}
	maxRate := ^uint32(0)
	if _, err := PCM16Header(int(maxRate), 0); err == nil {
		t.Fatal("overflowing byte rate accepted")
	}
}
