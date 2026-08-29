package services

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

type sessionRecordingRunnerInferencer struct {
	events                   []messages.StreamMessage
	waitForInput             bool
	waitForPrompt            bool
	waitForAudioEndAfterOpen bool
	connects                 int
	sessions                 []*sessionRecordingRunnerSession
}

func newSessionRecordingRunnerInferencer(events []messages.StreamMessage, waitForInput, waitForPrompt bool) *sessionRecordingRunnerInferencer {
	return &sessionRecordingRunnerInferencer{
		events:        append([]messages.StreamMessage(nil), events...),
		waitForInput:  waitForInput,
		waitForPrompt: waitForPrompt,
	}
}

func newSessionRecordingRunnerInferencerAfterAudioEnd(events []messages.StreamMessage) *sessionRecordingRunnerInferencer {
	return &sessionRecordingRunnerInferencer{
		events:                   append([]messages.StreamMessage(nil), events...),
		waitForAudioEndAfterOpen: true,
	}
}

func (i *sessionRecordingRunnerInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects++
	session := &sessionRecordingRunnerSession{
		receive:      messages.NewTypedBuffer[messages.StreamMessage](64),
		done:         make(chan struct{}),
		inputSeen:    make(chan struct{}),
		audioEndSeen: make(chan struct{}),
		promptSeen:   make(chan struct{}),
	}
	i.sessions = append(i.sessions, session)
	go func() {
		if i.waitForAudioEndAfterOpen {
			if !session.receive.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeSessionOpen,
				Value: messages.NewSessionOpenValue("runner-session", "session"),
			}) {
				return
			}
			select {
			case <-session.audioEndSeen:
			case <-ctx.Done():
				return
			}
		} else if i.waitForInput {
			select {
			case <-session.inputSeen:
			case <-ctx.Done():
				return
			}
			session.receive.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeSessionOpen,
				Value: messages.NewSessionOpenValue("runner-session", "session"),
			})
		} else if !session.receive.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("runner-session", "session"),
		}) {
			return
		}
		if i.waitForPrompt {
			select {
			case <-session.promptSeen:
			case <-ctx.Done():
				return
			}
		}
		for _, event := range i.events {
			if !session.receive.Write(ctx, event) {
				return
			}
		}
	}()
	return session, nil
}

type sessionRecordingRunnerSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once

	inputSeen             chan struct{}
	inputOnce             sync.Once
	audioEndSeen          chan struct{}
	audioEndOnce          sync.Once
	promptSeen            chan struct{}
	promptOnce            sync.Once
	sentMu                sync.Mutex
	sent                  []messages.StreamMessage
	imageMessages         []messages.Message
	imageResponseRequests []bool
	sendHook              func(context.Context, messages.StreamMessage)
}

func (s *sessionRecordingRunnerSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	s.sentMu.Lock()
	s.sent = append(s.sent, msg)
	s.sentMu.Unlock()
	if msg.Type == messages.StreamTypeAudioDelta {
		s.inputOnce.Do(func() { close(s.inputSeen) })
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		s.audioEndOnce.Do(func() { close(s.audioEndSeen) })
	}
	if msg.Type == messages.StreamTypeTextDelta {
		s.promptOnce.Do(func() { close(s.promptSeen) })
	}
	if s.sendHook != nil {
		s.sendHook(ctx, msg)
	}
	return true
}

func (s *sessionRecordingRunnerSession) SendMessage(_ context.Context, msg messages.Message) bool {
	return s.sendImageMessage(msg, true)
}

func (s *sessionRecordingRunnerSession) SendMessageWithoutResponse(_ context.Context, msg messages.Message) bool {
	return s.sendImageMessage(msg, false)
}

func (s *sessionRecordingRunnerSession) sendImageMessage(msg messages.Message, requestResponse bool) bool {
	s.sentMu.Lock()
	s.imageMessages = append(s.imageMessages, msg)
	s.imageResponseRequests = append(s.imageResponseRequests, requestResponse)
	s.sentMu.Unlock()
	s.promptOnce.Do(func() { close(s.promptSeen) })
	return true
}

func (s *sessionRecordingRunnerSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionRecordingRunnerSession) Done() <-chan struct{} { return s.done }

func (s *sessionRecordingRunnerSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *sessionRecordingRunnerSession) sentMessagesCopy() []messages.StreamMessage {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func (s *sessionRecordingRunnerSession) imageMessagesCopy() []messages.Message {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	return append([]messages.Message(nil), s.imageMessages...)
}

func (s *sessionRecordingRunnerSession) imageResponseRequestsCopy() []bool {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	return append([]bool(nil), s.imageResponseRequests...)
}

type sessionRecordingTestSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	sent    *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
}

func newSessionRecordingTestSession() *sessionRecordingTestSession {
	return &sessionRecordingTestSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](32),
		sent:    messages.NewTypedBuffer[messages.StreamMessage](32),
		done:    make(chan struct{}),
	}
}

func (s *sessionRecordingTestSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.sent.Write(ctx, msg)
}

func (s *sessionRecordingTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionRecordingTestSession) Done() <-chan struct{} { return s.done }

func (s *sessionRecordingTestSession) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

type persistentSessionRecordingInferencer struct {
	turns    [][]messages.StreamMessage
	connects int
}

func newPersistentSessionRecordingInferencer(turns [][]messages.StreamMessage) *persistentSessionRecordingInferencer {
	return &persistentSessionRecordingInferencer{turns: turns}
}

func (i *persistentSessionRecordingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects++
	session := &persistentSessionRecordingSession{
		turns:   i.turns,
		receive: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("persistent-session", "session"),
	}) {
		return nil, ctx.Err()
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("persistent-session"),
	}) {
		return nil, ctx.Err()
	}
	return session, nil
}

type persistentSessionRecordingSession struct {
	turns   [][]messages.StreamMessage
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
	turn    int
}

func (s *persistentSessionRecordingSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeMessageEnd || s.turn >= len(s.turns) {
		return true
	}
	for _, event := range s.turns[s.turn] {
		if !s.receive.Write(ctx, event) {
			return false
		}
	}
	s.turn++
	if s.turn == len(s.turns) {
		return s.receive.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("persistent-session", "done"),
		})
	}
	return true
}

func (s *persistentSessionRecordingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *persistentSessionRecordingSession) Done() <-chan struct{} {
	return s.done
}

func (s *persistentSessionRecordingSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type countingSessionRecordingInferencer struct{ connects int }

func (i *countingSessionRecordingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.connects++
	return newSessionRecordingTestSession(), nil
}

type failingSessionRecordingInferencer struct{ err error }

func (i *failingSessionRecordingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, i.err
}

func recordingEntries(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatalf("walk recording: %v", err)
	}
	sort.Strings(entries)
	return entries
}

func readSessionRecordingTranscript(t *testing.T, path string) []transcript.Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript %s: %v", path, err)
	}
	var records []transcript.Record
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		record, err := transcript.Decode(line)
		if err != nil {
			t.Fatalf("decode transcript %s: %v", path, err)
		}
		records = append(records, record)
	}
	return records
}

func threeRecordingDigits(index int) string {
	return fmt.Sprintf("%03d", index)
}

var _ messages.Session = (*sessionRecordingTestSession)(nil)
var _ messages.SessionInferencer = (*countingSessionRecordingInferencer)(nil)
var _ messages.SessionInferencer = (*persistentSessionRecordingInferencer)(nil)
