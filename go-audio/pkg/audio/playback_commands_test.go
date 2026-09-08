package audio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestPlaybackCommandReceiptIsIndependentOfFullPCMBuffer(t *testing.T) {
	data, _, _, err := NewFrameBuffer(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.TrySubmit(PCMFrame{Samples: []int16{1}}); err != nil {
		t.Fatal(err)
	}
	commands, err := NewPlaybackCommands(1)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	done := make(chan PlaybackReceipt, 1)
	go func() { done <- commands.Exchange(context.Background(), PlaybackInterruptActive, PlaybackResponse{}) }()
	req, err := commands.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("admission was reported as application")
	default:
	}
	req.Complete(PlaybackReceipt{Applied: true, Interruption: PlaybackInterruption{AudioEndMS: 42}})
	receipt := <-done
	if !receipt.Applied || receipt.CommandID != req.ID || receipt.Interruption.AudioEndMS != 42 {
		t.Fatalf("receipt=%+v", receipt)
	}
}
func TestPlaybackCommandCloseReleasesOutstandingReceipt(t *testing.T) {
	commands, _ := NewPlaybackCommands(1)
	done := make(chan PlaybackReceipt, 1)
	go func() { done <- commands.Exchange(context.Background(), PlaybackResume, PlaybackResponse{}) }()
	req, err := commands.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	commands.Close()
	if receipt := <-done; !errors.Is(receipt.Err, ErrClosed) {
		t.Fatal(receipt)
	}
	// A late worker receipt never blocks after its requester stopped waiting.
	req.Complete(PlaybackReceipt{Applied: true})
	req.Complete(PlaybackReceipt{Applied: true})
}
func TestPlaybackCommandPreCancelledRequestIsNotAdmitted(t *testing.T) {
	commands, _ := NewPlaybackCommands(1)
	defer commands.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	receipt := commands.Exchange(ctx, PlaybackResume, PlaybackResponse{})
	if !errors.Is(receipt.Err, context.Canceled) || len(commands.requests) != 0 {
		t.Fatalf("receipt=%+v queued=%d", receipt, len(commands.requests))
	}
}

func TestPlaybackCommandsTrySubmitAdmitsInterruptWithoutWaiting(t *testing.T) {
	commands, err := NewPlaybackCommands(1)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	if err := commands.TrySubmit(Command{ID: 41, Epoch: 7, Kind: CommandInterrupt}); err != nil {
		t.Fatal(err)
	}
	if err := commands.TrySubmit(Command{ID: 42, Epoch: 8, Kind: CommandInterrupt}); !errors.Is(err, ErrControlFull) {
		t.Fatalf("second admission = %v, want ErrControlFull", err)
	}
	request, err := commands.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if request.Operation != PlaybackDiscard || request.ID != 41 || request.Epoch != 7 {
		t.Fatalf("request = %+v, want interrupt identity preserved", request)
	}
	request.Complete(PlaybackReceipt{Applied: true})
}

func TestPlaybackCommandsTrySubmitRejectsUnsupportedDrain(t *testing.T) {
	commands, err := NewPlaybackCommands(1)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	if err := commands.TrySubmit(Command{Kind: CommandDrain}); !errors.Is(err, ErrUnsupportedPlaybackCommand) {
		t.Fatalf("drain admission = %v, want ErrUnsupportedPlaybackCommand", err)
	}
}

func TestPlaybackCommandsTrySubmitWithReceiptPreservesIdentity(t *testing.T) {
	commands, err := NewPlaybackCommands(1)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	receipts, err := commands.TrySubmitWithReceipt(Command{ID: 73, Epoch: 9, Kind: CommandInterrupt})
	if err != nil {
		t.Fatal(err)
	}
	request, err := commands.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request.Complete(PlaybackReceipt{Applied: true})
	select {
	case receipt := <-receipts:
		if receipt.CommandID != 73 || !receipt.Applied {
			t.Fatalf("receipt = %+v", receipt)
		}
	default:
		t.Fatal("worker receipt was not observable")
	}
}

func TestPlaybackCommandsTrySubmitDeliversAppliedReceiptToObserver(t *testing.T) {
	commands, err := NewPlaybackCommands(1)
	if err != nil {
		t.Fatal(err)
	}
	defer commands.Close()
	receipts := make(chan PlaybackReceipt, 1)
	commands.SetReceiptObserver(func(receipt PlaybackReceipt) { receipts <- receipt })
	if err := commands.TrySubmit(Command{ID: 91, Epoch: 4, Kind: CommandInterrupt}); err != nil {
		t.Fatal(err)
	}
	request, err := commands.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request.Complete(PlaybackReceipt{Applied: true})
	select {
	case receipt := <-receipts:
		if receipt.CommandID != 91 || !receipt.Applied {
			t.Fatalf("receipt = %+v", receipt)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not receive worker receipt")
	}
}

func TestCommandBufferKeepsControlBoundedAndClosesDeterministically(t *testing.T) {
	var nilProducer CommandProducer
	if err := nilProducer.TrySubmit(Command{Kind: CommandInterrupt}); !errors.Is(err, ErrBufferClosed) {
		t.Fatalf("nil producer submit = %v, want ErrBufferClosed", err)
	}
	producer, consumer, err := NewCommandBuffer(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.TrySubmit(Command{ID: 7, Epoch: 3, Kind: CommandInterrupt}); err != nil {
		t.Fatalf("first command submit = %v", err)
	}
	if err := producer.TrySubmit(Command{ID: 8, Kind: CommandDrain}); !errors.Is(err, ErrControlFull) {
		t.Fatalf("full command submit = %v, want ErrControlFull", err)
	}
	command, err := consumer.Receive(context.Background())
	if err != nil || command.ID != 7 || command.Epoch != 3 {
		t.Fatalf("received command = %+v, %v", command, err)
	}
	producer.Close()
	if _, err := consumer.Receive(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("receive after close = %v, want io.EOF", err)
	}
	if err := producer.TrySubmit(Command{}); !errors.Is(err, ErrBufferClosed) {
		t.Fatalf("submit after close = %v, want ErrBufferClosed", err)
	}
}

func TestBufferedOutboundCopiesFramesAndReportsClosedProducer(t *testing.T) {
	producer, consumer, control, err := NewFrameBuffer(1, 4)
	if err != nil {
		t.Fatal(err)
	}
	control.Invalidate(4)
	outbound := BufferedOutbound{Producer: producer}
	input := PCMFrame{Samples: []int16{1, 2}, StreamID: "capture", Epoch: 4}
	if err := outbound.WriteFrame(context.Background(), input); err != nil {
		t.Fatalf("write buffered frame = %v", err)
	}
	input.Samples[0] = 99
	got, err := consumer.Receive(context.Background())
	if err != nil || !reflect.DeepEqual(got.Samples, []int16{1, 2}) || got.StreamID != "capture" || got.Epoch != 4 {
		t.Fatalf("received buffered frame = %+v, %v", got, err)
	}
	if err := outbound.Close(); err != nil {
		t.Fatalf("close buffered outbound: %v", err)
	}
	if err := outbound.WriteFrame(context.Background(), PCMFrame{}); !errors.Is(err, ErrBufferClosed) {
		t.Fatalf("write after outbound close = %v, want ErrBufferClosed", err)
	}
}

func TestPlaybackProcessorFlushesTailAndDropsBlockedGeneration(t *testing.T) {
	format := PCM16DeviceFormat(SampleRate)
	processor, err := NewPlaybackProcessor(format, format, FrameSize)
	if err != nil {
		t.Fatal(err)
	}
	if frames, err := processor.Process(PCMFrame{Samples: []int16{1, 2, 3}}, 7, false); err != nil || len(frames) != 0 {
		t.Fatalf("partial process = %#v, %v; want buffered tail", frames, err)
	}
	frames, err := processor.Flush(7, false)
	if err != nil || len(frames) != 1 || !reflect.DeepEqual(frames[0].Samples, []int16{1, 2, 3}) || !frames[0].EndOfResponse || frames[0].Epoch != 7 {
		t.Fatalf("flush = %#v, %v; want exact generation-7 tail", frames, err)
	}
	if frames, err := processor.Process(PCMFrame{Samples: []int16{9}}, 8, true); err != nil || len(frames) != 0 {
		t.Fatalf("blocked generation process = %#v, %v", frames, err)
	}
	if frames, err := processor.Flush(8, true); err != nil || len(frames) != 0 {
		t.Fatalf("blocked generation flush = %#v, %v", frames, err)
	}
	frames, err = processor.Process(PCMFrame{Samples: []int16{11}, EndOfResponse: true}, 8, false)
	if err != nil || len(frames) != 1 || frames[0].Epoch != 8 || !reflect.DeepEqual(frames[0].Samples, []int16{11}) {
		t.Fatalf("new generation process = %#v, %v", frames, err)
	}
}

func TestFileSourcePreservesCountAwareTail(t *testing.T) {
	input := []int16{10, -20, 30}
	source, err := NewFileSource("-", bytes.NewReader(pcmBytes(input)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := source.Close(); err != nil {
			t.Errorf("source.Close(): %v", err)
		}
	}()
	buf := make([]int16, 2)
	count, err := source.ReadSamples(context.Background(), buf)
	if err != nil || count != 2 || !reflect.DeepEqual(buf, []int16{10, -20}) {
		t.Fatalf("raw first samples = %d %v %v", count, buf, err)
	}
	count, err = source.ReadSamples(context.Background(), buf)
	if err != nil || count != 1 || buf[0] != 30 {
		t.Fatalf("raw tail samples = %d %v %v", count, buf, err)
	}
	if count, err := source.ReadSamples(context.Background(), buf); !errors.Is(err, io.EOF) || count != 0 {
		t.Fatalf("raw samples after tail = %d %v, want EOF", count, err)
	}
}

func TestWAVSourcePreservesCountAwareTail(t *testing.T) {
	input := []int16{10, -20, 30}
	var encoded bytes.Buffer
	if err := wavio.Write(&encoded, SampleRate, input); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "input.wav")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	wav, err := NewWAVSource(path, file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := wav.Close(); err != nil {
			t.Errorf("wav.Close(): %v", err)
		}
	}()
	if wav.SampleRate() != SampleRate {
		t.Fatalf("WAV source rate = %d, want %d", wav.SampleRate(), SampleRate)
	}
	buf := make([]int16, 2)
	count, err := wav.ReadSamples(context.Background(), buf)
	if err != nil || count != 2 || !reflect.DeepEqual(buf, []int16{10, -20}) {
		t.Fatalf("WAV first samples = %d %v %v", count, buf, err)
	}
	count, err = wav.ReadSamples(context.Background(), buf)
	if err != nil || count != 1 || buf[0] != 30 {
		t.Fatalf("WAV tail samples = %d %v %v", count, buf, err)
	}
	if count, err := wav.ReadSamples(context.Background(), buf); !errors.Is(err, io.EOF) || count != 0 {
		t.Fatalf("WAV samples after tail = %d %v, want EOF", count, err)
	}
}

func TestDeviceFormatValuesRemainComparableAndFresh(t *testing.T) {
	defaultFormat := DefaultDeviceFormat()
	if !defaultFormat.Equal(PCM16DeviceFormat(SampleRate)) {
		t.Fatalf("default format = %+v, want standard PCM16", defaultFormat)
	}
	formats := DefaultDeviceFormatAvailability()
	if len(formats) != 1 || !formats[0].Equal(defaultFormat) {
		t.Fatalf("available formats = %+v, want one default format", formats)
	}
	formats[0].SampleRate = 24_000
	if DefaultDeviceFormatAvailability()[0].SampleRate != SampleRate {
		t.Fatal("default device format availability leaked mutable backing storage")
	}
	if defaultFormat.Equal(PCM16DeviceFormat(24_000)) {
		t.Fatal("formats with different rates compare equal")
	}
}
