package fakelive_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

func TestStartAnswersSessionStartedWithServerDefaults(t *testing.T) {
	c := dial(t, fakelive.New(fakelive.WithAPIKey(testKey)))
	started := c.start(liveConfig())
	want := live.SessionResource{
		ID: fakelive.DefaultSessionID, Status: live.SessionStatusActive, ExpiresAt: fakelive.DefaultExpiresAt,
		SessionConfig: live.SessionConfig{
			Model: live.Model1, Instructions: "Be concise.", Input: []live.InitialItem{},
			Audio: &live.SessionAudio{
				Format: &live.AudioFormat{Type: live.AudioTypePCM, Rate: live.RatePCM24k},
				Output: &live.AudioOutput{Voice: live.Voice{Name: fakelive.DefaultVoice}},
			},
			Delegation: &live.Delegation{Type: live.DelegationClient},
		},
	}
	if started.ClientEventID != "evt_start" || !reflect.DeepEqual(started.Session, want) {
		t.Fatalf("session.started = %#v, want resolved defaults %#v", started, want)
	}
}

func TestStartRejectsInvalidSessionsAndStaysUnstarted(t *testing.T) {
	for name, tc := range map[string]struct {
		frame, code, param, clientEventID string
	}{
		"command first":       {`{"type":"session.input_audio.mute","event_id":"m1"}`, fakelive.CodeSessionStartRequired, "type", "m1"},
		"turn detection":      {`{"type":"session.start","event_id":"s1","session":{"model":"gpt-live-1","turn_detection":{}}}`, live.CodeUnknownParameter, "session.turn_detection", "s1"},
		"top-level tools":     {`{"type":"session.start","session":{"model":"gpt-live-1","tools":[]}}`, live.CodeUnknownParameter, "session.tools", ""},
		"nested format key":   {`{"type":"session.start","session":{"model":"gpt-live-1","audio":{"format":{"type":"audio/pcm","rate":24000,"channels":1}}}}`, live.CodeUnknownParameter, "session.audio.format.channels", ""},
		"history part key":    {`{"type":"session.start","session":{"model":"gpt-live-1","input":[{"role":"user","content":[{"type":"input_text","text":"a","lang":"en"}]}]}}`, live.CodeUnknownParameter, "session.input.content.lang", ""},
		"envelope key":        {`{"type":"session.start","model":"gpt-live-1","session":{"model":"gpt-live-1"}}`, live.CodeUnknownParameter, "model", ""},
		"no session":          {`{"type":"session.start"}`, fakelive.CodeMissingRequiredParameter, "session", ""},
		"no model":            {`{"type":"session.start","session":{}}`, fakelive.CodeMissingRequiredParameter, "session.model", ""},
		"realtime model":      {`{"type":"session.start","session":{"model":"gpt-realtime"}}`, fakelive.CodeInvalidValue, "session.model", ""},
		"bad rate":            {`{"type":"session.start","session":{"model":"gpt-live-1","audio":{"format":{"type":"audio/pcm","rate":44100}}}}`, fakelive.CodeInvalidValue, "session.audio.format", ""},
		"bad g711 rate":       {`{"type":"session.start","session":{"model":"gpt-live-1","audio":{"format":{"type":"audio/pcmu","rate":16000}}}}`, fakelive.CodeInvalidValue, "session.audio.format", ""},
		"bad format type":     {`{"type":"session.start","session":{"model":"gpt-live-1","audio":{"format":{"type":"audio/opus","rate":48000}}}}`, fakelive.CodeInvalidValue, "session.audio.format", ""},
		"responses no model":  {`{"type":"session.start","session":{"model":"gpt-live-1","delegation":{"type":"responses","responses":{}}}}`, fakelive.CodeMissingRequiredParameter, "session.delegation.responses.model", ""},
		"bad delegation":      {`{"type":"session.start","session":{"model":"gpt-live-1","delegation":{"type":"server"}}}`, fakelive.CodeInvalidValue, "session.delegation.type", ""},
		"malformed":           {`{"type":`, fakelive.CodeMalformedEvent, "", ""},
		"unknown event first": {`{"type":"session.reset","event_id":"r1"}`, fakelive.CodeSessionStartRequired, "type", "r1"},
	} {
		t.Run(name, func(t *testing.T) {
			c := dial(t, fakelive.New())
			c.sendRaw(tc.frame)
			c.expectError(tc.code, tc.param, tc.clientEventID)
			c.start(liveConfig())
		})
	}
}

func TestBuiltSessionStartIsAcceptedAndSmuggledKeysAreRefused(t *testing.T) {
	cfg := liveConfig()
	cfg.Voice = "cedar"
	cfg.InputAudioFormat, cfg.OutputAudioFormat = models.AudioFormatG711Alaw, models.AudioFormatG711Alaw
	cfg.Config = json.RawMessage(`{"store":true,"delegation":{"type":"responses","responses":{"model":"gpt-6-astra","tools":[{"type":"web_search"}]}},
		"input":[{"role":"user","content":[{"type":"input_text","text":"Hi."}]}]}`)
	c := dial(t, fakelive.New())
	started := c.start(cfg)
	if started.Session.Audio.Format.Type != live.AudioTypePCMA || started.Session.Audio.Output.Voice.Name != "cedar" ||
		started.Session.Delegation.Responses.Model != "gpt-6-astra" || !*started.Session.Store {
		t.Fatalf("session.started = %#v, want the built settings echoed", started.Session)
	}

	start, err := live.BuildSessionStart("evt_smuggle", liveConfig())
	if err != nil {
		t.Fatal(err)
	}
	frame, err := live.EncodeEvent(start)
	if err != nil {
		t.Fatal(err)
	}
	smuggled := strings.Replace(string(frame), `"model":`, `"input_audio_transcription":{"model":"x"},"model":`, 1)
	other := dial(t, fakelive.New())
	other.sendRaw(smuggled)
	other.expectError(live.CodeUnknownParameter, "session.input_audio_transcription", "evt_smuggle")
}

func TestSecondSessionStartIsRefused(t *testing.T) {
	c := dial(t, fakelive.New())
	c.start(liveConfig())
	c.send(live.SessionStart{EventID: "again", Session: live.SessionConfig{Model: live.Model1}})
	c.expectError(fakelive.CodeSessionAlreadyStarted, "type", "again")
}

func TestInputAudioIsRecordedAndOddPCMIsRefused(t *testing.T) {
	server := fakelive.New()
	c := dial(t, server)
	c.start(liveConfig())
	second := bytes.Repeat([]byte{1, 0}, live.RatePCM24k) // one second of 24 kHz PCM16
	half := second[:len(second)/2]
	for _, chunk := range [][]byte{half, half} {
		event, err := live.NewInputAudioAppend(chunk, live.AudioFormat{Type: live.AudioTypePCM, Rate: live.RatePCM24k})
		if err != nil {
			t.Fatal(err)
		}
		c.send(event)
	}
	c.send(live.InputAudioAppend{EventID: "odd", Audio: "AAAA"}) // three bytes
	c.expectError(live.CodeInvalidAudio, "audio", "odd")
	c.send(live.InputAudioAppend{EventID: "junk", Audio: "%%%"})
	c.expectError(live.CodeInvalidAudio, "audio", "junk")
	c.send(live.SessionClose{EventID: "bye"})
	closed := expect[live.SessionClosed](c)
	if closed.Reason != live.CloseReasonCloseRequested || closed.ClientEventID != "bye" || closed.Usage.Seconds != 1 {
		t.Fatalf("session.closed = %#v, want close_requested after one second of audio", closed)
	}
	c.expectClosedSocket()
	if !bytes.Equal(server.InputAudio(), second) {
		t.Fatalf("recorded %d bytes of audio, want the %d accepted bytes", len(server.InputAudio()), len(second))
	}
}

func TestG711AudioAdvancesTheTimelineOneBytePerSample(t *testing.T) {
	c := dial(t, fakelive.New())
	cfg := liveConfig()
	cfg.InputAudioFormat, cfg.OutputAudioFormat = models.AudioFormatG711Ulaw, models.AudioFormatG711Ulaw
	c.start(cfg)
	event, err := live.NewInputAudioAppend(make([]byte, live.RateG711/2+1), live.AudioFormat{Type: live.AudioTypePCMU, Rate: live.RateG711})
	if err != nil {
		t.Fatal(err)
	}
	c.send(event)
	c.send(live.InstructionsAppend{EventID: "i1", Content: "Greet the caller."})
	ack := expect[live.InstructionsAppended](c)
	if ack.StartMS != 500 || ack.EndMS != 500+fakelive.DefaultAckSpan.Milliseconds() {
		t.Fatalf("ack = %#v, want the timeline at 500 ms", ack)
	}
}

func TestAppendsAreAcknowledgedOnTheTimelineAndChecked(t *testing.T) {
	server := fakelive.New(fakelive.WithScript(
		fakelive.AwaitStarted(),
		fakelive.Send(live.DelegationCreated{EventID: "d", OffsetMS: 10, Delegation: live.DelegationInfo{ID: "del_1", Type: "delegation", Target: live.DelegationClient}}),
	))
	c := dial(t, server)
	c.start(liveConfig())
	expect[live.DelegationCreated](c)

	c.send(live.CommentaryAppend{EventID: "c1", DelegationID: ptr("del_1"), Content: "Table for two is free."})
	if ack := expect[live.CommentaryAppended](c); ack.ClientEventID != "c1" || ack.EndMS-ack.StartMS != fakelive.DefaultAckSpan.Milliseconds() {
		t.Fatalf("commentary ack = %#v", ack)
	}
	c.send(live.ThinkingAppend{EventID: "t1", Content: "Checking."})
	if ack := expect[live.ThinkingAppended](c); ack.ClientEventID != "t1" {
		t.Fatalf("thinking ack = %#v", ack)
	}
	c.send(live.InstructionsAppend{EventID: "i1", DelegationID: ptr("del_unknown"), Content: "x"})
	c.expectError(fakelive.CodeUnknownDelegation, "delegation_id", "i1")
	c.sendRaw(`{"type":"session.commentary.append","event_id":"c2","content":"no id"}`)
	c.expectError(fakelive.CodeMissingRequiredParameter, "delegation_id", "c2")
}

func TestWithheldAcksSimulateAStalledTimeline(t *testing.T) {
	c := dial(t, fakelive.New(fakelive.WithoutAcks(live.TypeCommentaryAppend)))
	c.start(liveConfig())
	c.send(live.CommentaryAppend{EventID: "c1", Content: "Hello."})
	c.send(live.InputAudioMute{EventID: "m1"})
	if ack := expect[live.InputAudioMuted](c); ack.ClientEventID != "m1" {
		t.Fatalf("mute ack = %#v", ack)
	}
	c.send(live.InputAudioUnmute{EventID: "u1"})
	if ack := expect[live.InputAudioUnmuted](c); ack.ClientEventID != "u1" {
		t.Fatalf("unmute ack = %#v", ack)
	}
}

func TestSessionUpdateChangesOnlyResponsesSettings(t *testing.T) {
	client := dial(t, fakelive.New())
	client.start(liveConfig())
	client.send(live.SessionUpdate{EventID: "u1", Session: live.SessionPatch{Delegation: &live.Delegation{Type: live.DelegationResponses}}})
	client.expectError(live.CodeImmutableFieldUpdate, "session.delegation.type", "u1")
	client.send(live.SessionUpdate{EventID: "u2"})
	if updated := expect[live.SessionUpdated](client); updated.ClientEventID != "u2" || updated.Session.Delegation.Type != live.DelegationClient {
		t.Fatalf("empty update = %#v", updated)
	}

	cfg := liveConfig()
	cfg.Config = json.RawMessage(`{"delegation":{"type":"responses","responses":{"model":"gpt-6-astra","instructions":"Old.","service_tier":"auto"}}}`)
	responses := dial(t, fakelive.New())
	responses.start(cfg)
	patch := live.ResponsesDelegationConfig{
		Model: "gpt-6-nova", Instructions: ptr("New."), MaxOutputTokens: ptr(1024), ParallelToolCalls: ptr(true),
		Reasoning: &live.Reasoning{Effort: "low"}, ServiceTier: "priority", Text: &live.TextConfig{Verbosity: "low"},
		ToolChoice: json.RawMessage(`"auto"`), Tools: []live.Tool{{Type: "web_search"}},
	}
	responses.send(live.SessionUpdate{EventID: "u3", Session: live.SessionPatch{Delegation: &live.Delegation{Type: live.DelegationResponses, Responses: &patch}}})
	updated := expect[live.SessionUpdated](responses)
	if got := *updated.Session.Delegation.Responses; !reflect.DeepEqual(got, patch) {
		t.Fatalf("merged responses = %#v, want %#v", got, patch)
	}
	responses.send(live.SessionUpdate{EventID: "u4", Session: live.SessionPatch{Delegation: &live.Delegation{
		Type: live.DelegationResponses, Responses: &live.ResponsesDelegationConfig{MaxOutputTokens: ptr(64)}}}})
	kept := expect[live.SessionUpdated](responses).Session.Delegation.Responses
	if kept.Model != "gpt-6-nova" || *kept.Instructions != "New." || *kept.MaxOutputTokens != 64 {
		t.Fatalf("sparse update = %#v, want only max_output_tokens changed", kept)
	}
}

func TestUnmodelledAndMalformedCommandsGetErrors(t *testing.T) {
	server := fakelive.New()
	c := dial(t, server)
	c.start(liveConfig())
	c.send(live.ResponseItemCreate{EventID: "r1", Item: json.RawMessage(`{"type":"message"}`)})
	c.send(live.ResponseCreate{EventID: "r2"})
	c.sendRaw(`{"type":"session.rewind","event_id":"x1"}`)
	c.expectError(fakelive.CodeUnknownEventType, "type", "x1")
	c.sendRaw(`not json`)
	c.expectError(fakelive.CodeMalformedEvent, "", "")
	types := []string{}
	for _, event := range server.ClientEvents() {
		types = append(types, event.EventType())
	}
	want := []string{live.TypeSessionStart, live.TypeResponseItemCreate, live.TypeResponseCreate, "session.rewind"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("recorded client events %v, want %v", types, want)
	}
}
