package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiooutput"
	goaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestServiceRawOutputProcessesAndObservesAssistantPCM(t *testing.T) {
	var output bytes.Buffer
	var observed []byte
	service := New()
	result, err := service.Open(audiooutput.Config{
		Path:       "-",
		Writer:     &output,
		SampleRate: goaudio.SampleRate,
		Loudness:   fixedPCMProcessor{content: []byte{0x07, 0x00, 0xf9, 0xff}},
		ObserveAudioOutput: func(content []byte, _ messages.StreamMessage) {
			observed = append(observed, content...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.WriteDelta(context.Background(), []byte{0x01, 0x00}, messages.StreamMessage{Type: messages.StreamTypeAudioDelta}); err != nil {
		t.Fatalf("WriteDelta: %v", err)
	}
	if err := result.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	want := []byte{0x07, 0x00, 0xf9, 0xff}
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("raw output = %x, want %x", output.Bytes(), want)
	}
	if !bytes.Equal(observed, want) {
		t.Fatalf("observed output = %x, want processed %x", observed, want)
	}
}

func TestServiceWAVOutputFinalizesAndRemovesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "response.wav")
	result, err := New().Open(audiooutput.Config{Path: path, Writer: io.Discard, SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	samples := []int16{0, 1, -2, 32767, -32768}
	if err := result.WriteDelta(context.Background(), pcm16(samples), messages.StreamMessage{}); err != nil {
		t.Fatalf("WriteDelta: %v", err)
	}
	if err := result.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rate, got, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("wavio.Read: %v", err)
	}
	if rate != 16000 || !equalSamples(got, samples) {
		t.Fatalf("WAV = rate %d samples %v, want rate 16000 samples %v", rate, got, samples)
	}

	emptyPath := filepath.Join(t.TempDir(), "empty.wav")
	empty, err := New().Open(audiooutput.Config{Path: emptyPath, Writer: io.Discard, SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("empty Close: %v", err)
	}
	if _, err := os.Stat(emptyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty WAV stat = %v, want removed file", err)
	}
}

func TestServiceDeviceBoundWritesConsumedSamplesAtNegotiatedRate(t *testing.T) {
	var output bytes.Buffer
	var observed []byte
	result, err := New().Open(audiooutput.Config{
		Path: "-", Writer: &output, DeviceBound: true,
		ObserveAudioOutput: func(content []byte, _ messages.StreamMessage) {
			observed = append(observed, content...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	delta := []byte{0x01, 0x00, 0x02, 0x00}
	if err := result.WriteDelta(context.Background(), delta, messages.StreamMessage{}); err != nil {
		t.Fatalf("device WriteDelta: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("device output wrote before consumption: %d bytes", output.Len())
	}
	if err := result.ObserveDeviceSamples(context.Background(), 48000, []int16{3, -4}); err != nil {
		t.Fatalf("ObserveDeviceSamples: %v", err)
	}
	if err := result.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !bytes.Equal(output.Bytes(), []byte{0x03, 0x00, 0xfc, 0xff}) {
		t.Fatalf("device output = %x, want consumed PCM16", output.Bytes())
	}
	if !bytes.Equal(observed, delta) {
		t.Fatalf("device observed = %x, want provider delta %x", observed, delta)
	}
}

func TestServiceRejectsInvalidConfigurationAndUnsupportedDeviceTap(t *testing.T) {
	if _, err := New().Open(audiooutput.Config{Path: "-"}); !errors.Is(err, audiooutput.ErrInvalidConfig) {
		t.Fatalf("invalid config error = %v, want ErrInvalidConfig", err)
	}
	result, err := New().Open(audiooutput.Config{Path: "-", Writer: io.Discard, SampleRate: goaudio.SampleRate})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := result.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	if err := result.ObserveDeviceSamples(context.Background(), goaudio.SampleRate, []int16{1}); !errors.Is(err, audiooutput.ErrDeviceObserverUnavailable) {
		t.Fatalf("non-device tap error = %v, want ErrDeviceObserverUnavailable", err)
	}
}

func TestInferencerRetainsAudioAcceptedAcrossCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &barrierInferencer{session: newBarrierSession()}
	writer := &signalWriter{written: make(chan struct{})}
	result, err := New().Open(audiooutput.Config{Path: "-", Writer: writer, SampleRate: goaudio.SampleRate})
	if err != nil {
		t.Fatal(err)
	}
	inferencer := newInferencer(provider, result, audiooutput.SessionOptions{})
	session, err := inferencer.ConnectSession(ctx)
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- session.Close() }()
	want := append(bytes.Repeat([]byte{0x01, 0x02}, 32), bytes.Repeat([]byte{0x03, 0x04}, 48)...)
	if !provider.session.recv.Write(context.Background(), messages.StreamMessage{
		Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant,
		Value: messages.NewAudioDeltaValue(want),
	}) {
		t.Fatal("provider rejected queued cancellation audio")
	}
	select {
	case <-writer.written:
	case <-time.After(2 * time.Second):
		t.Fatal("queued cancellation audio was not written")
	}
	select {
	case <-provider.session.closeStarted:
		t.Fatal("provider closed before queued cancellation audio drained")
	default:
	}
	provider.session.releaseClose()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("session Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session Close did not finish")
	}
	if !bytes.Equal(writer.data.Bytes(), want) {
		t.Fatalf("retained output = %x, want %x", writer.data.Bytes(), want)
	}
}

func TestServiceWrapPreservesOptionalSessionCapabilities(t *testing.T) {
	provider := &capabilityInferencer{session: newCapabilitySession()}
	result, err := New().Open(audiooutput.Config{Path: "-", Writer: io.Discard, SampleRate: goaudio.SampleRate})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := result.Close(); err != nil {
			t.Errorf("output Close: %v", err)
		}
	}()
	inferencer := New().Wrap(provider, result, audiooutput.SessionOptions{
		WirePrompt: "wire-prompt", SeedValue: "seed-value",
		AdaptSession: func(session messages.Session) messages.Session { return session },
	})
	session, err := inferencer.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	assertSeedForwarding(t, session, provider.session)
	assertResponseCapability(t, session)
	assertMessageCapabilities(t, session)
	assertCompleteCapabilities(t, session)
	assertMediaAndTerminal(t, session, provider.session.terminalErr)
	if session.Receive() == nil || session.Done() == nil {
		t.Fatal("decorated session lifecycle channels are nil")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("session Close: %v", err)
	}
	inferencer.Wait()
	if inferencer.Err() != nil {
		t.Fatalf("inferencer Err = %v, want nil", inferencer.Err())
	}
}

func assertSeedForwarding(t *testing.T, session messages.Session, provider *capabilitySession) {
	t.Helper()
	if !session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("wire-prompt")}) {
		t.Fatal("Send returned false")
	}
	value, ok := provider.sent[0].Value.(*messages.TextDeltaValue)
	if !ok || value.Content != "seed-value" {
		t.Fatalf("seed content = %#v, want seed-value", provider.sent[0].Value)
	}
}

func assertResponseCapability(t *testing.T, session messages.Session) {
	t.Helper()
	if outcome := messages.RequestSessionResponse(context.Background(), session); !outcome.OK() {
		t.Fatalf("RequestSessionResponse = %#v, want success", outcome)
	}
	if !messages.SupportsSessionResponseRequests(session) {
		t.Fatal("response-request capability was not preserved")
	}
}

func assertMessageCapabilities(t *testing.T, session messages.Session) {
	t.Helper()
	sendMessage, ok := session.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	if !ok || !sendMessage.SendMessage(context.Background(), messages.Message{}) {
		t.Fatal("SendMessage capability was not preserved")
	}
	sendWithoutResponse, ok := session.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	if !ok || !sendWithoutResponse.SendMessageWithoutResponse(context.Background(), messages.Message{}) {
		t.Fatal("SendMessageWithoutResponse capability was not preserved")
	}
}

func assertCompleteCapabilities(t *testing.T, session messages.Session) {
	t.Helper()
	complete, ok := session.(interface{ SupportsCompleteMessages() bool })
	if !ok || !complete.SupportsCompleteMessages() {
		t.Fatal("complete-message capability was not preserved")
	}
	withoutResponse, ok := session.(interface{ SupportsCompleteMessagesWithoutResponse() bool })
	if !ok || !withoutResponse.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("complete-message no-response capability was not preserved")
	}
}

func assertMediaAndTerminal(t *testing.T, session messages.Session, wantErr error) {
	t.Helper()
	media, ok := session.(goaudio.MediaSession)
	if !ok || media.RTCMedia() != (goaudio.MediaEndpoints{}) {
		t.Fatalf("media capability = %#v, want zero media endpoints", session)
	}
	terminal, ok := session.(interface{ TerminalError() error })
	if !ok {
		t.Fatal("TerminalError capability was not preserved")
	}
	if got := terminal.TerminalError(); !errors.Is(got, wantErr) {
		t.Fatalf("TerminalError = %v, want provider error", got)
	}
}

type fixedPCMProcessor struct{ content []byte }

func (p fixedPCMProcessor) ProcessBytes([]byte) []byte { return append([]byte(nil), p.content...) }

type barrierInferencer struct{ session *barrierSession }

func (i *barrierInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type barrierSession struct {
	recv         *messages.TypedBuffer[messages.StreamMessage]
	done         chan struct{}
	closeStarted chan struct{}
	release      chan struct{}
	once         sync.Once
}

func newBarrierSession() *barrierSession {
	return &barrierSession{
		recv:         messages.NewTypedBuffer[messages.StreamMessage](32),
		done:         make(chan struct{}),
		closeStarted: make(chan struct{}),
		release:      make(chan struct{}),
	}
}

func (s *barrierSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *barrierSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *barrierSession) Done() <-chan struct{} { return s.done }

func (s *barrierSession) Close() error {
	s.once.Do(func() {
		close(s.closeStarted)
		<-s.release
		close(s.done)
	})
	return nil
}

func (s *barrierSession) releaseClose() {
	select {
	case <-s.release:
	default:
		close(s.release)
	}
}

type capabilityInferencer struct{ session *capabilitySession }

func (i *capabilityInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type capabilitySession struct {
	recv        *messages.TypedBuffer[messages.StreamMessage]
	done        chan struct{}
	sent        []messages.StreamMessage
	terminalErr error
	closeOnce   sync.Once
}

func newCapabilitySession() *capabilitySession {
	return &capabilitySession{recv: messages.NewTypedBuffer[messages.StreamMessage](8), done: make(chan struct{}), terminalErr: errors.New("terminal capability error")}
}

func (s *capabilitySession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.sent = append(s.sent, msg)
	return true
}

func (s *capabilitySession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *capabilitySession) Done() <-chan struct{} { return s.done }

func (s *capabilitySession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *capabilitySession) RequestResponse(context.Context) messages.SessionSendOutcome {
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (*capabilitySession) SupportsResponseRequests() bool { return true }

func (s *capabilitySession) SendMessage(context.Context, messages.Message) bool { return true }

func (s *capabilitySession) SendMessageWithoutResponse(context.Context, messages.Message) bool {
	return true
}

func (*capabilitySession) SupportsCompleteMessages() bool { return true }

func (*capabilitySession) SupportsCompleteMessagesWithoutResponse() bool { return true }

func (s *capabilitySession) TerminalError() error { return s.terminalErr }

func (s *capabilitySession) RTCMedia() goaudio.MediaEndpoints { return goaudio.MediaEndpoints{} }

type signalWriter struct {
	data    bytes.Buffer
	written chan struct{}
	once    sync.Once
}

func (w *signalWriter) Write(data []byte) (int, error) {
	n, err := w.data.Write(data)
	if n > 0 {
		w.once.Do(func() { close(w.written) })
	}
	return n, err
}

func pcm16(samples []int16) []byte {
	result := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(result[index*2:], uint16(sample))
	}
	return result
}

func equalSamples(got, want []int16) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
