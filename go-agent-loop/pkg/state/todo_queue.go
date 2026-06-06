package state

// TodoQueue is a FIFO queue of string messages used to enqueue continuation
// nudges or other deferred work items during agent loop execution.
type TodoQueue struct {
	items []string
}

// Enqueue appends a message to the back of the queue.
func (q *TodoQueue) Enqueue(msg string) {
	q.items = append(q.items, msg)
}

// Dequeue removes and returns the first message from the queue.
// Returns false if the queue is empty.
func (q *TodoQueue) Dequeue() (string, bool) {
	if len(q.items) == 0 {
		return "", false
	}
	msg := q.items[0]
	q.items = q.items[1:]
	return msg, true
}

// Len returns the number of messages in the queue.
func (q *TodoQueue) Len() int {
	return len(q.items)
}
