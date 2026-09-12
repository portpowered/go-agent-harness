// Command imageinput-consumer proves that an external module can compose the
// public image-input service with an injected loader and fake provider session.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput"
	imageinputwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput/wire"
)

type contentLoader struct {
	parts map[string]messages.ContentPart
	errs  map[string]error
	calls int
}

func (loader *contentLoader) Load(_ context.Context, path string) (messages.ContentPart, error) {
	loader.calls++
	if err := loader.errs[path]; err != nil {
		return nil, err
	}
	return loader.parts[path], nil
}

type providerSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	messages  []messages.Message
	events    []messages.StreamMessage
	immediate int
	deferred  int
	complete  bool
	without   bool
	sendOK    bool
	closeOnce chan struct{}
}

func newProviderSession() *providerSession {
	return &providerSession{
		receive:   messages.NewTypedBuffer[messages.StreamMessage](8),
		done:      make(chan struct{}),
		sendOK:    true,
		closeOnce: make(chan struct{}),
	}
}

func (session *providerSession) Send(ctx context.Context, event messages.StreamMessage) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if !session.sendOK {
		return false
	}
	session.events = append(session.events, event)
	return true
}

func (session *providerSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return session.receive
}

func (session *providerSession) Done() <-chan struct{} { return session.done }

func (session *providerSession) Close() error {
	select {
	case <-session.closeOnce:
	default:
		close(session.closeOnce)
		close(session.done)
	}
	return nil
}

func (session *providerSession) SendMessage(_ context.Context, message messages.Message) bool {
	if !session.sendOK {
		return false
	}
	session.immediate++
	session.messages = append(session.messages, message)
	return true
}

func (session *providerSession) SendMessageWithoutResponse(_ context.Context, message messages.Message) bool {
	if !session.sendOK {
		return false
	}
	session.deferred++
	session.messages = append(session.messages, message)
	return true
}

func (session *providerSession) SupportsCompleteMessages() bool { return session.complete }

func (session *providerSession) SupportsCompleteMessagesWithoutResponse() bool {
	return session.without
}

type providerInferencer struct{ session *providerSession }

func (inferencer providerInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return inferencer.session, nil
}

type report struct {
	Schema             string   `json:"schema"`
	ConstructedVia     string   `json:"constructed_via"`
	PreparedMIMEs      []string `json:"prepared_mimes"`
	LoaderCalls        int      `json:"loader_calls"`
	ImmediateMessages  int      `json:"immediate_messages"`
	DeferredMessages   int      `json:"deferred_messages"`
	ForwardingExposed  bool     `json:"forwarding_exposed"`
	CancellationIsSend bool     `json:"cancellation_is_send"`
	CancellationIsCtx  bool     `json:"cancellation_is_context"`
	ShutdownIsSend     bool     `json:"shutdown_is_send"`
}

func main() {
	mode := "positive"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	result, err := run(mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}

func run(mode string) (report, error) {
	switch mode {
	case "positive":
		return runPositive()
	case "negative":
		return runNegative()
	case "wrong-oracle":
		return runWrongOracle()
	default:
		return report{}, fmt.Errorf("unknown consumer mode %q", mode)
	}
}

func runPositive() (report, error) {
	pngBytes := encodedPNG()
	jpegBytes := encodedJPEG()
	loader := &contentLoader{parts: map[string]messages.ContentPart{
		"png":  messages.ImagePart{Bytes: pngBytes, MediaType: " IMAGE/PNG "},
		"jpeg": messages.ImagePart{Bytes: jpegBytes, MediaType: "image/jpeg"},
	}}
	service := imageinputwire.NewService(loader)
	parts, err := service.Prepare(context.Background(), []string{"png", "jpeg"}, imageinput.Capabilities{
		Model:                   "consumer-vision",
		SupportsImageInput:      true,
		SupportedInputMIMETypes: []string{" IMAGE/PNG ", "image/jpeg", "IMAGE/PNG"},
	})
	if err != nil {
		return report{}, fmt.Errorf("prepare public image service: %w", err)
	}
	if len(parts) != 2 || parts[0].MediaType != "image/png" || parts[1].MediaType != "image/jpeg" {
		return report{}, fmt.Errorf("unexpected normalized parts: %#v", parts)
	}
	parts[0].Bytes[0] ^= 0xff
	originalPNG := loader.parts["png"].(messages.ImagePart).Bytes
	if parts[0].Bytes[0] == originalPNG[0] {
		return report{}, errors.New("prepared image bytes alias loader-owned bytes")
	}
	parts[0].Bytes = append([]byte(nil), originalPNG...)

	provider := newProviderSession()
	provider.complete, provider.without = true, true
	attachment, err := service.Attach(providerInferencer{session: provider}, parts, imageinput.TurnOptions{})
	if err != nil {
		return report{}, fmt.Errorf("attach public image service: %w", err)
	}
	parts[0].Bytes = []byte("caller mutation after attach")
	connected, err := attachment.Inferencer.ConnectSession(context.Background())
	if err != nil {
		return report{}, fmt.Errorf("connect public image service: %w", err)
	}
	if !connected.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}) {
		return report{}, errors.New("public consumer could not forward prior stream event")
	}
	if !connected.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("describe both")}) {
		return report{}, errors.New("public consumer image turn was rejected")
	}
	if len(provider.messages) != 1 || provider.immediate != 1 {
		return report{}, fmt.Errorf("immediate publication = %#v, count=%d", provider.messages, provider.immediate)
	}
	if provider.messages[0].TextContent() != "describe both" || len(provider.messages[0].ContentParts) != 3 {
		return report{}, fmt.Errorf("ordered consumer message = %#v", provider.messages[0])
	}
	imagePart, ok := provider.messages[0].ContentParts[1].(messages.ImagePart)
	if !ok || !bytes.Equal(imagePart.Bytes, originalPNG) {
		return report{}, errors.New("attached image was not immutable across caller mutation")
	}
	select {
	case firstTurn := <-attachment.FirstTurn:
		if firstTurn != nil {
			return report{}, fmt.Errorf("successful first-turn signal = %v", firstTurn)
		}
	case <-time.After(time.Second):
		return report{}, errors.New("public consumer first-turn signal timed out")
	}
	if _, ok := connected.(imageinput.ForwardingSession); !ok {
		return report{}, errors.New("public consumer lost forwarding capability surface")
	}
	if !connected.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("later")}) {
		return report{}, errors.New("public consumer did not forward later stream event")
	}

	deferredProvider := newProviderSession()
	deferredProvider.complete, deferredProvider.without = true, true
	deferred, err := service.Attach(providerInferencer{session: deferredProvider}, parts, imageinput.TurnOptions{DeferResponse: true})
	if err != nil {
		return report{}, fmt.Errorf("attach deferred public image service: %w", err)
	}
	deferredSession, err := deferred.Inferencer.ConnectSession(context.Background())
	if err != nil {
		return report{}, fmt.Errorf("connect deferred public image service: %w", err)
	}
	if !deferredSession.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(imageinput.ImageOnlyPrompt)}) {
		return report{}, errors.New("deferred image turn was rejected")
	}
	if deferredProvider.deferred != 1 || deferredProvider.messages[0].TextContent() != imageinput.DeferredInstruction {
		return report{}, fmt.Errorf("deferred publication = %#v", deferredProvider.messages)
	}

	return report{
		Schema:            "audio-runtime-c67-image-input-consumer/v1",
		ConstructedVia:    "imageinput/wire.NewService",
		PreparedMIMEs:     []string{parts[0].MediaType, parts[1].MediaType},
		LoaderCalls:       loader.calls,
		ImmediateMessages: provider.immediate,
		DeferredMessages:  deferredProvider.deferred,
		ForwardingExposed: true,
	}, nil
}

func runNegative() (report, error) {
	pngBytes := encodedPNG()
	loader := &contentLoader{
		parts: map[string]messages.ContentPart{
			"unsupported": messages.FilePart{Bytes: []byte("file"), MediaType: "text/plain"},
			"invalid":     messages.ImagePart{Bytes: []byte("not an image"), MediaType: "image/png"},
			"empty":       messages.ImagePart{MediaType: "image/png"},
			"png":         messages.ImagePart{Bytes: pngBytes, MediaType: "image/png"},
		},
		errs: map[string]error{"missing": os.ErrNotExist},
	}
	service := imageinputwire.NewService(loader)
	checks := []struct {
		path  string
		kind  imageinput.ErrorKind
		check func(error) bool
	}{
		{path: "missing", kind: imageinput.ErrMissingFile, check: func(err error) bool { var target *imageinput.MissingFileError; return errors.As(err, &target) }},
		{path: "unsupported", kind: imageinput.ErrUnsupportedMIME, check: func(err error) bool { var target *imageinput.UnsupportedMIMEError; return errors.As(err, &target) }},
		{path: "invalid", kind: imageinput.ErrInvalidContent, check: func(err error) bool { var target *imageinput.InvalidContentError; return errors.As(err, &target) }},
		{path: "empty", kind: imageinput.ErrEmptyFile, check: func(err error) bool { var target *imageinput.EmptyFileError; return errors.As(err, &target) }},
	}
	for _, check := range checks {
		_, err := service.Prepare(context.Background(), []string{check.path}, imageinput.Capabilities{SupportsImageInput: true})
		if !errors.Is(err, check.kind) || !check.check(err) {
			return report{}, fmt.Errorf("negative %s = %v", check.path, err)
		}
	}
	provider := newProviderSession()
	provider.complete = true
	provider.sendOK = false
	attachment, err := service.Attach(providerInferencer{session: provider}, []messages.ImagePart{{Bytes: pngBytes, MediaType: "image/png"}}, imageinput.TurnOptions{})
	if err != nil {
		return report{}, err
	}
	connected, err := attachment.Inferencer.ConnectSession(context.Background())
	if err != nil {
		return report{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if connected.Send(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("cancelled")}) {
		return report{}, errors.New("cancelled public send unexpectedly succeeded")
	}
	firstTurn := <-attachment.FirstTurn
	if !errors.Is(firstTurn, imageinput.ErrSend) || !errors.Is(firstTurn, context.Canceled) {
		return report{}, fmt.Errorf("cancelled first-turn identity = %v", firstTurn)
	}
	shutdownProvider := newProviderSession()
	shutdownAttachment, err := service.Attach(providerInferencer{session: shutdownProvider}, []messages.ImagePart{{Bytes: pngBytes, MediaType: "image/png"}}, imageinput.TurnOptions{})
	if err != nil {
		return report{}, err
	}
	shutdownSession, err := shutdownAttachment.Inferencer.ConnectSession(context.Background())
	if err != nil {
		return report{}, err
	}
	if err := shutdownSession.Close(); err != nil {
		return report{}, err
	}
	shutdown := <-shutdownAttachment.FirstTurn
	if !errors.Is(shutdown, imageinput.ErrSend) {
		return report{}, fmt.Errorf("shutdown first-turn identity = %v", shutdown)
	}
	return report{
		Schema:             "audio-runtime-c67-image-input-consumer/negative-v1",
		ConstructedVia:     "imageinput/wire.NewService",
		CancellationIsSend: true,
		CancellationIsCtx:  errors.Is(firstTurn, context.Canceled),
		ShutdownIsSend:     true,
	}, nil
}

func runWrongOracle() (report, error) {
	if os.Getenv("C67_WRONG_ORACLE") != "1" {
		return report{}, errors.New("wrong-oracle mode requires C67_WRONG_ORACLE=1")
	}
	_, err := runNegative()
	if err != nil {
		return report{}, err
	}
	return report{}, errors.New("wrong oracle expected cancelled image publication to lose context identity")
}

func encodedPNG() []byte {
	var buffer bytes.Buffer
	_ = png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	return buffer.Bytes()
}

func encodedJPEG() []byte {
	var buffer bytes.Buffer
	_ = jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil)
	return buffer.Bytes()
}
