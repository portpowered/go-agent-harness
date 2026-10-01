package clock

// deterministicTimerHeap is a binary min-heap of pending deterministic timers
// ordered by deadline and then by creation sequence. Each timer records its
// heap index so a stopped timer can be removed in O(log n). The heap is typed
// (not container/heap) so only timers can ever be stored.
type deterministicTimerHeap []*deterministicTimer

func (h *deterministicTimerHeap) Len() int { return len(*h) }

func (h *deterministicTimerHeap) less(i, j int) bool {
	a, b := (*h)[i], (*h)[j]
	if a.deadlineElapsed != b.deadlineElapsed {
		return a.deadlineElapsed < b.deadlineElapsed
	}
	return a.sequence < b.sequence
}

func (h *deterministicTimerHeap) swap(i, j int) {
	timers := *h
	timers[i], timers[j] = timers[j], timers[i]
	timers[i].index, timers[j].index = i, j
}

// push adds timer and restores heap order.
func (h *deterministicTimerHeap) push(timer *deterministicTimer) {
	timer.index = len(*h)
	*h = append(*h, timer)
	h.up(timer.index)
}

// remove deletes the timer at index i; remove(0) pops the earliest timer.
func (h *deterministicTimerHeap) remove(i int) {
	last := len(*h) - 1
	if i != last {
		h.swap(i, last)
		if !h.down(i, last) {
			h.up(i)
		}
	}
	timer := (*h)[last]
	(*h)[last] = nil
	timer.index = -1
	*h = (*h)[:last]
}

func (h *deterministicTimerHeap) up(child int) {
	for child > 0 {
		parent := (child - 1) / 2
		if !h.less(child, parent) {
			return
		}
		h.swap(parent, child)
		child = parent
	}
}

// down sifts the element at start toward the leaves within the first n
// elements and reports whether it moved.
func (h *deterministicTimerHeap) down(start, n int) bool {
	current := start
	for {
		left := 2*current + 1
		if left >= n || left < 0 {
			break
		}
		smallest := left
		if right := left + 1; right < n && h.less(right, left) {
			smallest = right
		}
		if !h.less(smallest, current) {
			break
		}
		h.swap(current, smallest)
		current = smallest
	}
	return current > start
}
