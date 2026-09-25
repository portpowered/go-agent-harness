package events

import (
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestBrokerDropsOnlySlowSubscriberWithoutBlockingPublishers(t *testing.T) {
	broker := newTestBroker(t, []string{"a"}, Options{QueueSize: 1})
	slow := subscribe(t, broker, "")
	fast := subscribe(t, broker, "a")

	// The slow subscriber never reads: one frame fits in its bounded queue and
	// the next retires it. Publishing must return promptly regardless, and the
	// subscriber that keeps up must keep receiving.
	for _, event := range []string{"first", "queued", "overflow"} {
		publishWithin(t, func() { broker.Diagnostic("a", event, nil) })
		if got := frameString(t, nextFrame(t, fast), "event"); got != event {
			t.Fatalf("fast subscriber event = %q, want %q", got, event)
		}
	}
	if first := frameString(t, nextFrame(t, slow), "event"); first != "first" {
		t.Fatalf("slow subscriber first frame = %q", first)
	}
	if _, open := <-slow.Frames(); open {
		t.Fatal("slow subscriber remained registered after queue overflow")
	}
}

func publishWithin(t *testing.T, publish func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		publish()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishing to a slow subscriber blocked the room")
	}
}

func TestBrokerPreservesPerParticipantOrderDuringConcurrentPublish(t *testing.T) {
	const count = 64
	broker := newTestBroker(t, []string{"a", "b"}, Options{QueueSize: count * 2})
	all := subscribe(t, broker, "")
	var publishers sync.WaitGroup
	for _, participant := range []string{"a", "b"} {
		participant := participant
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			for sequence := 0; sequence < count; sequence++ {
				broker.Diagnostic(participant, "ordered", map[string]string{"sequence": strconv.Itoa(sequence)})
			}
		}()
	}
	publishers.Wait()
	sequences := map[string][]int{"a": {}, "b": {}}
	for index := 0; index < count*2; index++ {
		payload := nextFrame(t, all)
		var fields map[string]string
		if err := json.Unmarshal(payload["fields"], &fields); err != nil {
			t.Fatalf("ordered fields: %v", err)
		}
		sequence, err := strconv.Atoi(fields["sequence"])
		if err != nil {
			t.Fatalf("ordered sequence = %v: %v", fields, err)
		}
		participant := frameString(t, payload, "participant_id")
		sequences[participant] = append(sequences[participant], sequence)
	}
	for participant, got := range sequences {
		if len(got) != count {
			t.Fatalf("participant %s event count = %d, want %d", participant, len(got), count)
		}
		for index, sequence := range got {
			if sequence != index {
				t.Fatalf("participant %s sequence at %d = %d; all=%v", participant, index, sequence, got)
			}
		}
	}
}
