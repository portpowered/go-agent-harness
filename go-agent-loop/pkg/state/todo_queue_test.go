package state

import "testing"

func TestTodoQueue_EnqueueDequeue(t *testing.T) {
	var q TodoQueue

	q.Enqueue("first")
	q.Enqueue("second")
	q.Enqueue("third")

	if q.Len() != 3 {
		t.Fatalf("expected Len()=3, got %d", q.Len())
	}

	msg, ok := q.Dequeue()
	if !ok || msg != "first" {
		t.Fatalf("expected (first, true), got (%q, %v)", msg, ok)
	}

	msg, ok = q.Dequeue()
	if !ok || msg != "second" {
		t.Fatalf("expected (second, true), got (%q, %v)", msg, ok)
	}

	msg, ok = q.Dequeue()
	if !ok || msg != "third" {
		t.Fatalf("expected (third, true), got (%q, %v)", msg, ok)
	}

	if q.Len() != 0 {
		t.Fatalf("expected Len()=0 after draining, got %d", q.Len())
	}
}

func TestTodoQueue_DequeueEmpty(t *testing.T) {
	var q TodoQueue

	msg, ok := q.Dequeue()
	if ok {
		t.Fatalf("expected ok=false on empty queue, got (%q, true)", msg)
	}
	if msg != "" {
		t.Fatalf("expected empty string on empty queue, got %q", msg)
	}
}

func TestTodoQueue_FIFOOrder(t *testing.T) {
	var q TodoQueue

	items := []string{"a", "b", "c", "d", "e"}
	for _, item := range items {
		q.Enqueue(item)
	}

	for i, expected := range items {
		msg, ok := q.Dequeue()
		if !ok {
			t.Fatalf("dequeue %d: expected ok=true", i)
		}
		if msg != expected {
			t.Fatalf("dequeue %d: expected %q, got %q", i, expected, msg)
		}
	}

	// Queue should now be empty
	_, ok := q.Dequeue()
	if ok {
		t.Fatal("expected ok=false after draining queue")
	}
}

func TestTodoQueue_InterleavedEnqueueDequeue(t *testing.T) {
	var q TodoQueue

	q.Enqueue("first")
	msg, ok := q.Dequeue()
	if !ok || msg != "first" {
		t.Fatalf("expected (first, true), got (%q, %v)", msg, ok)
	}

	q.Enqueue("second")
	q.Enqueue("third")

	msg, ok = q.Dequeue()
	if !ok || msg != "second" {
		t.Fatalf("expected (second, true), got (%q, %v)", msg, ok)
	}

	q.Enqueue("fourth")

	msg, ok = q.Dequeue()
	if !ok || msg != "third" {
		t.Fatalf("expected (third, true), got (%q, %v)", msg, ok)
	}

	msg, ok = q.Dequeue()
	if !ok || msg != "fourth" {
		t.Fatalf("expected (fourth, true), got (%q, %v)", msg, ok)
	}

	if q.Len() != 0 {
		t.Fatalf("expected empty queue, got Len()=%d", q.Len())
	}
}
