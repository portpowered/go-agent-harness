package openailive_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
)

func TestUnknownEventTypesDecodeToUnknownEventAndReEncodeUnchanged(t *testing.T) {
	frame := []byte(`{"type":"session.future_feature","event_id":"evt_9","detail":{"a":1}}`)
	for name, decode := range map[string]func([]byte) (live.Event, error){
		"server": live.DecodeServerEvent,
		"client": live.DecodeClientEvent,
	} {
		t.Run(name, func(t *testing.T) {
			event, err := decode(frame)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			unknown, ok := event.(live.UnknownEvent)
			if !ok || unknown.EventType() != "session.future_feature" {
				t.Fatalf("decoded %#v, want UnknownEvent of session.future_feature", event)
			}
			encoded, err := live.EncodeEvent(event)
			if err != nil || !bytes.Equal(encoded, frame) {
				t.Fatalf("re-encoded %s, %v; want the original frame", encoded, err)
			}
		})
	}
}

func TestServerOnlyTypeIsUnknownToTheClientDecoderAndViceVersa(t *testing.T) {
	if event, err := live.DecodeClientEvent([]byte(`{"type":"session.started","session":{}}`)); err != nil {
		t.Fatal(err)
	} else if _, ok := event.(live.UnknownEvent); !ok {
		t.Fatalf("client decoder gave %T for a server event, want UnknownEvent", event)
	}
	if event, err := live.DecodeServerEvent([]byte(`{"type":"session.close"}`)); err != nil {
		t.Fatal(err)
	} else if _, ok := event.(live.UnknownEvent); !ok {
		t.Fatalf("server decoder gave %T for a client event, want UnknownEvent", event)
	}
}

func TestSharedInputAudioTypeDecodesByDirection(t *testing.T) {
	frame := []byte(`{"type":"session.input_audio.append","audio":"AAA="}`)
	server, err := live.DecodeServerEvent(frame)
	if err != nil {
		t.Fatal(err)
	}
	if reflected, ok := server.(live.ReflectedInputAudio); !ok || reflected.Audio != "AAA=" {
		t.Fatalf("server decode = %#v, want the sideband's reflected input", server)
	}
	client, err := live.DecodeClientEvent(frame)
	if err != nil {
		t.Fatal(err)
	}
	if appended, ok := client.(live.InputAudioAppend); !ok || appended.Audio != "AAA=" {
		t.Fatalf("client decode = %#v, want the primary append command", client)
	}
}

func TestMalformedFramesAreRejected(t *testing.T) {
	for name, frame := range map[string]string{
		"not json":         `not json`,
		"array":            `[1,2]`,
		"null":             `null`,
		"missing type":     `{"event_id":"evt_1"}`,
		"empty type":       `{"type":""}`,
		"numeric type":     `{"type":7}`,
		"mistyped field":   `{"type":"session.commentary.appended","start_ms":"soon"}`,
		"mistyped session": `{"type":"session.started","session":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := live.DecodeServerEvent([]byte(frame)); !errors.Is(err, live.ErrMalformedEvent) {
				t.Fatalf("server decode error = %v, want ErrMalformedEvent", err)
			}
		})
	}
	if _, err := live.DecodeClientEvent([]byte(`{"type":"session.start","session":{"model":7}}`)); !errors.Is(err, live.ErrMalformedEvent) {
		t.Fatalf("client decode error = %v, want ErrMalformedEvent", err)
	}
	if _, err := live.DecodeClientEvent([]byte(`{}`)); !errors.Is(err, live.ErrMalformedEvent) {
		t.Fatalf("client decode of a typeless frame = %v, want ErrMalformedEvent", err)
	}
}

func TestEncodeWritesTypeFirstAndHandlesBodylessEvents(t *testing.T) {
	encoded, err := live.EncodeEvent(live.SessionClose{})
	if err != nil || string(encoded) != `{"type":"session.close"}` {
		t.Fatalf("bodyless encode = %s, %v", encoded, err)
	}
	encoded, err = live.EncodeEvent(live.InputAudioMute{EventID: "evt_1"})
	if err != nil || string(encoded) != `{"type":"session.input_audio.mute","event_id":"evt_1"}` {
		t.Fatalf("encode = %s, %v", encoded, err)
	}
	if _, err := live.EncodeEvent(nil); !errors.Is(err, live.ErrMalformedEvent) {
		t.Fatalf("nil encode error = %v, want ErrMalformedEvent", err)
	}
}

func TestAppendsAlwaysCarryDelegationID(t *testing.T) {
	for _, event := range []live.Event{
		live.InstructionsAppend{Content: "Greet the caller."},
		live.ThinkingAppend{Content: "Looking it up."},
		live.CommentaryAppend{Content: "It is sunny."},
	} {
		encoded, err := live.EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"delegation_id":null`) {
			t.Fatalf("%s encodes as %s, want an explicit null delegation_id", event.EventType(), encoded)
		}
	}
	encoded, err := live.EncodeEvent(live.CommentaryAppend{DelegationID: ptr("del_1"), Content: "Done."})
	if err != nil || !strings.Contains(string(encoded), `"delegation_id":"del_1"`) {
		t.Fatalf("encode = %s, %v; want the delegation id", encoded, err)
	}
}

func TestInputAudioAppendRefusesOddPCMButPassesG711(t *testing.T) {
	pcm := live.AudioFormat{Type: live.AudioTypePCM, Rate: live.RatePCM24k}
	if _, err := live.NewInputAudioAppend([]byte{1, 2, 3}, pcm); !errors.Is(err, live.ErrOddPCMLength) {
		t.Fatalf("odd PCM error = %v, want ErrOddPCMLength", err)
	}
	for _, format := range []live.AudioFormat{
		{Type: live.AudioTypePCMU, Rate: live.RateG711},
		{Type: live.AudioTypePCMA, Rate: live.RateG711},
		pcm,
	} {
		audio := []byte{0x7f, 0x80, 0x00, 0xff}
		if format.Type != live.AudioTypePCM {
			audio = audio[:3]
		}
		event, err := live.NewInputAudioAppend(audio, format)
		if err != nil {
			t.Fatalf("%s: %v", format.Type, err)
		}
		decoded, err := event.Bytes()
		if err != nil || !bytes.Equal(decoded, audio) {
			t.Fatalf("%s round trip = %v, %v; want %v", format.Type, decoded, err, audio)
		}
	}
}

func TestAudioBytesRejectInvalidBase64(t *testing.T) {
	if _, err := (live.InputAudioAppend{Audio: "%%%"}).Bytes(); err == nil {
		t.Fatal("invalid input audio decoded")
	}
	if _, err := (live.OutputAudioDelta{Delta: "%%%"}).Bytes(); err == nil {
		t.Fatal("invalid output audio decoded")
	}
	audio, err := (live.OutputAudioDelta{Delta: "AACAAA=="}).Bytes()
	if err != nil || !bytes.Equal(audio, []byte{0, 0, 0x80, 0}) {
		t.Fatalf("output audio = %v, %v", audio, err)
	}
}

func TestCustomVoiceRoundTripsAsObject(t *testing.T) {
	frame := []byte(`{"type":"session.start","session":{"model":"gpt-live-1","audio":{"output":{"voice":{"id":"voice_custom_1"}}}}}`)
	event, err := live.DecodeClientEvent(frame)
	if err != nil {
		t.Fatal(err)
	}
	start, ok := event.(live.SessionStart)
	if !ok || start.Session.Audio.Output.Voice != (live.Voice{ID: "voice_custom_1"}) {
		t.Fatalf("decoded %#v, want the custom voice id", event)
	}
	encoded, err := live.EncodeEvent(start)
	if err != nil || !sameJSON(t, encoded, frame) {
		t.Fatalf("re-encoded %s, %v; want %s", encoded, err, frame)
	}
	if _, err := live.DecodeClientEvent([]byte(`{"type":"session.start","session":{"audio":{"output":{"voice":7}}}}`)); !errors.Is(err, live.ErrMalformedEvent) {
		t.Fatalf("numeric voice error = %v, want ErrMalformedEvent", err)
	}
}

func TestEveryEventTypeRoundTripsThroughItsDecoder(t *testing.T) {
	events := []live.Event{
		live.InputAudioUnmute{EventID: "e1"},
		live.InputAudioUnmuted{EventID: "e2", ClientEventID: "e1"},
		live.InstructionsAppended{EventID: "e3", StartMS: 1, EndMS: 2},
		live.ThinkingAppended{EventID: "e4", ClientEventID: "c4", StartMS: 3, EndMS: 4},
		live.TransportRinging{EventID: "e5", SessionID: "s"},
		live.TransportAnswered{EventID: "e6", SessionID: "s"},
		live.DTMFSend{EventID: "e7", Key: "#"},
		live.OutputAudioDelta{Delta: "AAA="},
	}
	for _, event := range events {
		encoded, err := live.EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		decode := live.DecodeServerEvent
		if event.EventType() == live.TypeInputAudioUnmute {
			decode = live.DecodeClientEvent
		}
		decoded, err := decode(encoded)
		if err != nil || !reflect.DeepEqual(decoded, event) {
			t.Fatalf("%s round trip = %#v, %v", event.EventType(), decoded, err)
		}
	}
}

func TestResponseEventDelegationIDKeepsAbsentNullAndString(t *testing.T) {
	for name, tc := range map[string]struct {
		frame string
		want  live.OptionalID
	}{
		"absent": {`{"type":"response.event","event":{}}`, live.OptionalID{}},
		"null":   {`{"type":"response.event","delegation_id":null,"event":{}}`, live.OptionalID{Present: true}},
		"string": {`{"type":"response.event","delegation_id":"del_1","event":{}}`, live.SomeID("del_1")},
	} {
		t.Run(name, func(t *testing.T) {
			event, err := live.DecodeServerEvent([]byte(tc.frame))
			if err != nil {
				t.Fatal(err)
			}
			got, ok := event.(live.ResponseEvent)
			if !ok || !reflect.DeepEqual(got.DelegationID, tc.want) {
				t.Fatalf("decoded %#v, want delegation id %#v", event, tc.want)
			}
			encoded, err := live.EncodeEvent(got)
			if err != nil || !sameJSON(t, encoded, []byte(tc.frame)) {
				t.Fatalf("re-encoded %s, %v; want %s", encoded, err, tc.frame)
			}
		})
	}
	if _, err := live.DecodeServerEvent([]byte(`{"type":"response.event","delegation_id":7}`)); !errors.Is(err, live.ErrMalformedEvent) {
		t.Fatalf("numeric delegation id error = %v, want ErrMalformedEvent", err)
	}
}

func TestServerEventsKeepClientEventID(t *testing.T) {
	for _, frame := range []string{
		`{"type":"error","event_id":"e","client_event_id":"c","error":{"type":"invalid_request_error","message":"m"}}`,
		`{"type":"info","client_event_id":"c","code":"x","message":"m"}`,
		`{"type":"session.usage.updated","client_event_id":"c","usage":{"seconds":1}}`,
		`{"type":"response.event","client_event_id":"c","event":{}}`,
		`{"type":"transport.ringing","client_event_id":"c","session_id":"s"}`,
		`{"type":"transport.failed","client_event_id":"c","session_id":"s","error":{"type":"call_error","message":"m"}}`,
		`{"type":"transport.dtmf.send","client_event_id":"c","event":"1"}`,
	} {
		event, err := live.DecodeServerEvent([]byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := live.EncodeEvent(event)
		if err != nil || !sameJSON(t, encoded, []byte(frame)) {
			t.Fatalf("%s re-encoded %s, %v; want client_event_id kept", event.EventType(), encoded, err)
		}
	}
}
