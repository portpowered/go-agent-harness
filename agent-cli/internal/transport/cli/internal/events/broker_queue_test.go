package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

type blockingResponseWriter struct {
	header       http.Header
	ready        chan struct{}
	writeStarted chan struct{}
	unblock      chan struct{}
	readyOnce    sync.Once
	writeOnce    sync.Once
}

func newBlockingResponseWriter() *blockingResponseWriter {
	return &blockingResponseWriter{header: make(http.Header), ready: make(chan struct{}), writeStarted: make(chan struct{}, 1), unblock: make(chan struct{})}
}

func (w *blockingResponseWriter) Header() http.Header { return w.header }
func (w *blockingResponseWriter) WriteHeader(int)     {}
func (w *blockingResponseWriter) Write(payload []byte) (int, error) {
	w.writeOnce.Do(func() { w.writeStarted <- struct{}{} })
	<-w.unblock
	return len(payload), nil
}
func (w *blockingResponseWriter) Flush() { w.readyOnce.Do(func() { close(w.ready) }) }

func readOrderedDiagnostic(t *testing.T, reader *sseReader) (string, int) {
	t.Helper()
	payload := reader.next(t)
	var fields map[string]string
	if err := json.Unmarshal(payload["fields"], &fields); err != nil {
		t.Fatalf("ordered fields: %v", err)
	}
	sequence, err := strconv.Atoi(fields["sequence"])
	if err != nil {
		t.Fatalf("ordered sequence = %v: %v", fields, err)
	}
	return sseString(t, payload, "participant_id"), sequence
}

func assertOrderedSequences(t *testing.T, sequences map[string][]int, count int) {
	t.Helper()
	for participant, got := range sequences {
		if len(got) != count {
			t.Fatalf("participant %s event count = %d, want %d", participant, len(got), count)
		}
		for index, sequence := range got {
			if sequence != index {
				t.Fatalf("participant %s sequence at %d = %d, want %d; all=%v", participant, index, sequence, index, got)
			}
		}
	}
}

func TestBrokerDropsSlowClientWithoutBlockingPublishers(t *testing.T) {
	broker, err := New([]string{"a"}, Options{QueueSize: 1})
	if err != nil {
		t.Fatalf("New broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("broker.Close(): %v", err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "http://room.test/events", nil).WithContext(ctx)
	writer := newBlockingResponseWriter()
	done := make(chan struct{})
	go func() { broker.ServeHTTP(writer, request); close(done) }()
	select {
	case <-writer.ready:
	case <-time.After(time.Second):
		t.Fatal("slow client handler did not register")
	}
	broker.RecordDiagnostic("a", "first", nil)
	select {
	case <-writer.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("slow client did not begin its blocked write")
	}
	broker.RecordDiagnostic("a", "queued", nil)
	publishDone := make(chan struct{})
	go func() {
		broker.RecordDiagnostic("a", "overflow", nil)
		close(publishDone)
	}()
	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("publishing to a slow client blocked the room")
	}
	close(writer.unblock)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dropped slow client handler did not settle")
	}
}

func TestBrokerDropsOnlySlowClientWhenQueueIsFull(t *testing.T) {
	broker, err := New([]string{"a"}, Options{QueueSize: 1})
	if err != nil {
		t.Fatalf("New broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("broker.Close(): %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "http://room.test/events", nil).WithContext(ctx)
	writer := newBlockingResponseWriter()
	done := make(chan struct{})
	go func() {
		broker.ServeHTTP(writer, request)
		close(done)
	}()
	select {
	case <-writer.ready:
	case <-time.After(time.Second):
		t.Fatal("slow client handler did not register")
	}

	// The first frame occupies the blocked writer. One more frame fits in the
	// bounded queue; the next frame retires this client and returns immediately
	// to the room publisher.
	broker.RecordDiagnostic("a", "first", nil)
	select {
	case <-writer.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("slow client did not begin its blocked write")
	}
	broker.RecordDiagnostic("a", "queued", nil)
	broker.RecordDiagnostic("a", "overflow", nil)

	deadline := time.Now().Add(time.Second)
	for {
		broker.mu.Lock()
		clients := len(broker.clients)
		broker.mu.Unlock()
		if clients == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow client remained registered after queue overflow")
		}
		time.Sleep(time.Millisecond)
	}

	close(writer.unblock)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dropped slow client handler did not settle")
	}
}

func TestBrokerPreservesPerParticipantOrderDuringConcurrentPublish(t *testing.T) {
	const count = 64
	broker, err := New([]string{"a", "b"}, Options{QueueSize: count * 2})
	if err != nil {
		t.Fatalf("New broker: %v", err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Errorf("broker.Close(): %v", err)
		}
	})
	server := httptest.NewServer(broker)
	defer server.Close()
	response, reader := openSSE(t, server, "/events")
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("concurrent-publish response Close(): %v", err)
		}
	})
	var publishers sync.WaitGroup
	for _, participant := range []string{"a", "b"} {
		participant := participant
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			for sequence := 0; sequence < count; sequence++ {
				broker.RecordDiagnostic(participant, "ordered", map[string]string{"sequence": strconv.Itoa(sequence)})
			}
		}()
	}
	publishers.Wait()
	sequences := map[string][]int{"a": {}, "b": {}}
	for index := 0; index < count*2; index++ {
		participant, sequence := readOrderedDiagnostic(t, reader)
		sequences[participant] = append(sequences[participant], sequence)
	}
	assertOrderedSequences(t, sequences, count)
}
