package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestSessionCommandAudioOutputMatrix(t *testing.T) {
	wantSamples := cliAudioOutputFrame()

	for _, testCase := range []struct {
		name       string
		fileOutput bool
		deviceOut  bool
	}{
		{name: "combined", fileOutput: true, deviceOut: true},
		{name: "file only", fileOutput: true},
		{name: "device only", deviceOut: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
			if err != nil {
				t.Fatalf("new virtual registry: %v", err)
			}

			var deviceObserver *audio.DeviceSource
			if testCase.deviceOut {
				deviceObserver, err = audio.NewDeviceSource(registry, "virtual:input")
				if err != nil {
					t.Fatalf("open virtual output observer: %v", err)
				}
				defer deviceObserver.Close()
			}

			inferencer := newCLIAudioOutputInferencer(wantSamples, testCase.deviceOut)
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = t.TempDir()
			command := NewSessionCommandWithDeviceRegistry(
				flags.NewAskFlags(),
				globalFlags,
				nil,
				inferencer,
				registry,
			).Generate()
			command.SetOut(io.Discard)

			audioOutPath := filepath.Join(t.TempDir(), "assistant.wav")
			args := []string{"--replay", "synthetic.json", "--prompt", "hello"}
			if testCase.fileOutput {
				args = append(args, "--audio-out", audioOutPath)
			}
			if testCase.deviceOut {
				args = append(args, "--audio-out-device", "virtual:output")
			}
			command.SetArgs(args)

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			runErr := make(chan error, 1)
			go func() {
				runErr <- command.ExecuteContext(ctx)
			}()

			if testCase.deviceOut {
				got := make([]int16, audio.FrameSize)
				readCtx, readCancel := context.WithTimeout(ctx, time.Second)
				readErr := deviceObserver.ReadFrame(readCtx, got)
				readCancel()
				if readErr != nil {
					inferencer.releaseClose()
					cancel()
					<-runErr
					t.Fatalf("device output read failed (session connects=%d registry=%+v): %v", inferencer.connects.Load(), registry.Observations(), readErr)
				}
				if !reflect.DeepEqual(got, wantSamples) {
					t.Fatalf("device output samples differ from assistant PCM")
				}
			}

			inferencer.releaseClose()
			select {
			case err := <-runErr:
				if err != nil {
					t.Fatalf("session command: %v (registry=%+v)", err, registry.Observations())
				}
			case <-ctx.Done():
				t.Fatalf("session command timed out (registry=%+v)", registry.Observations())
			}

			if got := inferencer.connects.Load(); got != 1 {
				t.Fatalf("provider session connects = %d, want exactly one", got)
			}
			if got := inferencer.sessionCloseCount.Load(); got != 1 {
				t.Fatalf("provider session closes = %d, want exactly one", got)
			}

			if testCase.fileOutput {
				data, err := os.ReadFile(audioOutPath)
				if err != nil {
					t.Fatalf("read captured file output: %v", err)
				}
				if len(data) == 0 {
					t.Fatal("captured file output is empty")
				}
				_, got, err := wavio.Read(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("parse captured WAV: %v", err)
				}
				if !reflect.DeepEqual(got, wantSamples) {
					t.Fatalf("captured file samples differ from assistant PCM")
				}
			} else if _, err := os.Stat(audioOutPath); !os.IsNotExist(err) {
				t.Fatalf("device-only file sink stat error = %v, want file to remain absent", err)
			}

			observations := registry.Observations()
			if testCase.deviceOut {
				if observations.OpenCount != 2 || observations.ReleaseCount != 1 {
					t.Fatalf("device observations before observer close = %+v, want two opens and one release", observations)
				}
				if err := deviceObserver.Close(); err != nil {
					t.Fatalf("close virtual output observer: %v", err)
				}
				observations = registry.Observations()
				if observations.OpenCount != 2 || observations.ReleaseCount != 2 {
					t.Fatalf("device observations after cleanup = %+v, want balanced opens and releases", observations)
				}
			} else if observations.OpenCount != 0 || observations.ReleaseCount != 0 {
				t.Fatalf("file-only device observations = %+v, want no device acquisition", observations)
			}
		})
	}
}

func cliAudioOutputFrame() []int16 {
	frame := make([]int16, audio.FrameSize)
	for index := range frame {
		frame[index] = int16((index*37)%20000 - 10000)
	}
	return frame
}

type cliAudioOutputInferencer struct {
	audioPCM  []byte
	deviceOut bool

	connects          atomic.Int32
	sessionCloseCount atomic.Int32
	closeGate         chan struct{}
	closeGateOnce     sync.Once
}

func newCLIAudioOutputInferencer(samples []int16, holdClose bool) *cliAudioOutputInferencer {
	inferencer := &cliAudioOutputInferencer{
		audioPCM:  cliPCM16Bytes(samples),
		deviceOut: holdClose,
		closeGate: make(chan struct{}),
	}
	if !holdClose {
		inferencer.releaseClose()
	}
	return inferencer
}

func (i *cliAudioOutputInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects.Add(1)
	session := &cliAudioOutputSession{
		receive:    messages.NewTypedBuffer[messages.StreamMessage](16),
		done:       make(chan struct{}),
		audioPCM:   append([]byte(nil), i.audioPCM...),
		closeGate:  i.closeGate,
		closeCount: &i.sessionCloseCount,
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("cli-audio-output-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	return session, nil
}

func (i *cliAudioOutputInferencer) releaseClose() {
	i.closeGateOnce.Do(func() { close(i.closeGate) })
}

type cliAudioOutputSession struct {
	receive    *messages.TypedBuffer[messages.StreamMessage]
	done       chan struct{}
	audioPCM   []byte
	closeGate  <-chan struct{}
	closeCount *atomic.Int32

	audioOnce sync.Once
	closeOnce sync.Once
}

func (s *cliAudioOutputSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	default:
	}
	if msg.Type == messages.StreamTypeSessionClose {
		if !s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("cli-audio-output-session", "test complete"),
		}) {
			return false
		}
		select {
		case <-s.closeGate:
		case <-ctx.Done():
			return false
		}
		return s.Close() == nil
	}
	s.audioOnce.Do(func() {
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(s.audioPCM),
		})
		s.receive.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		})
	})
	return true
}

func (s *cliAudioOutputSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *cliAudioOutputSession) Done() <-chan struct{} { return s.done }

func (s *cliAudioOutputSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeCount.Add(1)
		close(s.done)
	})
	return nil
}

func (s *cliAudioOutputSession) RTCMedia() services.RTCMediaEndpoints {
	if s == nil {
		return services.RTCMediaEndpoints{}
	}
	return services.RTCMediaEndpoints{Inbound: &cliSingleFrameInboundMedia{samples: cliPCM16Samples(s.audioPCM)}}
}

type cliSingleFrameInboundMedia struct {
	samples []int16
	read    atomic.Bool
}

func (m *cliSingleFrameInboundMedia) ReadFrame(ctx context.Context) (rtc.PCMFrame, error) {
	select {
	case <-ctx.Done():
		return rtc.PCMFrame{}, ctx.Err()
	default:
	}
	if m.read.Swap(true) {
		return rtc.PCMFrame{}, io.EOF
	}
	return rtc.PCMFrame{Samples: append([]int16(nil), m.samples...)}, nil
}

func (*cliSingleFrameInboundMedia) Close() error { return nil }

func cliPCM16Bytes(samples []int16) []byte {
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	return encoded
}

func cliPCM16Samples(encoded []byte) []int16 {
	samples := make([]int16, len(encoded)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(encoded[index*2:]))
	}
	return samples
}

var _ messages.SessionInferencer = (*cliAudioOutputInferencer)(nil)
var _ messages.Session = (*cliAudioOutputSession)(nil)
var _ services.RTCMediaSession = (*cliAudioOutputSession)(nil)
var _ rtc.InboundMedia = (*cliSingleFrameInboundMedia)(nil)
