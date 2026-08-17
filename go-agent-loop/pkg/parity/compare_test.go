package parity

import (
	"reflect"
	"testing"
)

func TestCompareIdenticalRepresentativeProjection(t *testing.T) {
	expected := comparisonFixture()
	actual := comparisonFixture()
	if len(expected.Transcripts) == 0 || len(expected.Audio.Frames) == 0 || len(expected.ToolCalls) == 0 || len(expected.Terminal.Payload) == 0 {
		t.Fatalf("fixture must retain representative evidence: %+v", expected)
	}

	expectedBefore := cloneProjection(expected)
	actualBefore := cloneProjection(actual)
	if differences := Compare(expected, actual); len(differences) != 0 {
		t.Fatalf("identical projections returned differences: %#v", differences)
	}
	if !reflect.DeepEqual(expected, expectedBefore) || !reflect.DeepEqual(actual, actualBefore) {
		t.Fatal("Compare mutated a projection")
	}
}

func TestCompareRequiredDivergenceClasses(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(Projection) Projection
		want   []Difference
	}{
		{
			name: "missing record on actual side",
			mutate: func(actual Projection) Projection {
				actual.Transcripts = actual.Transcripts[:1]
				return actual
			},
			want: []Difference{{
				Path:     "transcripts[1]",
				Expected: `{"order":2,"payload":"d29ybGQ=","text":"world","tick":13}`,
				Actual:   "null",
			}},
		},
		{
			name: "extra record on actual side",
			mutate: func(actual Projection) Projection {
				actual.Transcripts = append(actual.Transcripts, TranscriptFact{Order: 3, Tick: 16, Text: "extra", Payload: []byte("extra")})
				return actual
			},
			want: []Difference{{
				Path:     "transcripts[2]",
				Expected: "null",
				Actual:   `{"order":3,"payload":"ZXh0cmE=","text":"extra","tick":16}`,
			}},
		},
		{
			name: "reordered adjacent records",
			mutate: func(actual Projection) Projection {
				actual.Transcripts[0], actual.Transcripts[1] = actual.Transcripts[1], actual.Transcripts[0]
				return actual
			},
			want: []Difference{
				{Path: "transcripts[0].order", Expected: "1", Actual: "2"},
				{Path: "transcripts[0].payload", Expected: `"aGVsbG8="`, Actual: `"d29ybGQ="`},
				{Path: "transcripts[0].text", Expected: `"hello"`, Actual: `"world"`},
				{Path: "transcripts[0].tick", Expected: "12", Actual: "13"},
				{Path: "transcripts[1].order", Expected: "2", Actual: "1"},
				{Path: "transcripts[1].payload", Expected: `"d29ybGQ="`, Actual: `"aGVsbG8="`},
				{Path: "transcripts[1].text", Expected: `"world"`, Actual: `"hello"`},
				{Path: "transcripts[1].tick", Expected: "13", Actual: "12"},
			},
		},
		{
			name: "changed retained payload byte",
			mutate: func(actual Projection) Projection {
				actual.Audio.Frames[0].Payload = []byte("b")
				return actual
			},
			want: []Difference{{
				Path:     "audio.frames[0].payload",
				Expected: `"YQ=="`,
				Actual:   `"Yg=="`,
			}},
		},
		{
			name: "changed logical tick",
			mutate: func(actual Projection) Projection {
				actual.Transcripts[0].Tick = 99
				return actual
			},
			want: []Difference{{
				Path:     "transcripts[0].tick",
				Expected: "12",
				Actual:   "99",
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Compare(comparisonFixture(), test.mutate(cloneProjection(comparisonFixture())))
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("differences = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestCompareAccumulatesThreeIndependentDifferencesInPathOrder(t *testing.T) {
	actual := cloneProjection(comparisonFixture())
	actual.Audio.Frames[0].Payload = []byte("b")
	actual.Terminal.Reason = "client_close"
	actual.Transcripts[0].Tick = 99

	want := []Difference{
		{Path: "audio.frames[0].payload", Expected: `"YQ=="`, Actual: `"Yg=="`},
		{Path: "terminal.reason", Expected: `"provider_close"`, Actual: `"client_close"`},
		{Path: "transcripts[0].tick", Expected: "12", Actual: "99"},
	}
	if got := Compare(comparisonFixture(), actual); !reflect.DeepEqual(got, want) {
		t.Fatalf("differences = %#v, want %#v", got, want)
	}
}

func comparisonFixture() Projection {
	return Projection{
		Turns: []TurnBoundary{{Order: 1, Tick: 10, Kind: "turn.start", Boundary: "start", ID: "turn-1", Role: "user", Payload: []byte("turn")}},
		Audio: AudioSummary{
			FrameCount: 1, TotalBytes: 3,
			Frames: []AudioFrame{{Tick: 11, Bytes: []byte{1, 2, 3}, Payload: []byte("a")}},
		},
		Transcripts: []TranscriptFact{
			{Order: 1, Tick: 12, Text: "hello", Payload: []byte("hello")},
			{Order: 2, Tick: 13, Text: "world", Payload: []byte("world")},
		},
		ToolCalls: []ToolCallFact{{
			Order: 1, ID: "tool-1", Name: "lookup", Arguments: []byte(`{"q":"x"}`), Result: []byte(`{"ok":true}`),
			CallTick: 14, ResultTick: 15, CallPayload: []byte("call"), ResultPayload: []byte("result"),
		}},
		Interruptions: []InterruptionFact{{Order: 1, Tick: 16, Reason: "barge-in", Provenance: "client", Payload: []byte("interrupt")}},
		Terminal:      &TerminalOutcome{Tick: 17, Reason: "provider_close", Provenance: "provider", Payload: []byte("terminal")},
	}
}

func cloneProjection(source Projection) Projection {
	clone := source
	clone.Turns = append([]TurnBoundary(nil), source.Turns...)
	for index := range clone.Turns {
		clone.Turns[index].Payload = append([]byte(nil), source.Turns[index].Payload...)
	}
	clone.Audio.Frames = append([]AudioFrame(nil), source.Audio.Frames...)
	for index := range clone.Audio.Frames {
		clone.Audio.Frames[index].Bytes = append([]byte(nil), source.Audio.Frames[index].Bytes...)
		clone.Audio.Frames[index].Payload = append([]byte(nil), source.Audio.Frames[index].Payload...)
	}
	clone.Transcripts = append([]TranscriptFact(nil), source.Transcripts...)
	for index := range clone.Transcripts {
		clone.Transcripts[index].Payload = append([]byte(nil), source.Transcripts[index].Payload...)
	}
	clone.ToolCalls = append([]ToolCallFact(nil), source.ToolCalls...)
	for index := range clone.ToolCalls {
		clone.ToolCalls[index].Arguments = append([]byte(nil), source.ToolCalls[index].Arguments...)
		clone.ToolCalls[index].Result = append([]byte(nil), source.ToolCalls[index].Result...)
		clone.ToolCalls[index].CallPayload = append([]byte(nil), source.ToolCalls[index].CallPayload...)
		clone.ToolCalls[index].ResultPayload = append([]byte(nil), source.ToolCalls[index].ResultPayload...)
	}
	clone.Interruptions = append([]InterruptionFact(nil), source.Interruptions...)
	for index := range clone.Interruptions {
		clone.Interruptions[index].Payload = append([]byte(nil), source.Interruptions[index].Payload...)
	}
	if source.Terminal != nil {
		terminal := *source.Terminal
		terminal.Payload = append([]byte(nil), source.Terminal.Payload...)
		clone.Terminal = &terminal
	}
	return clone
}
