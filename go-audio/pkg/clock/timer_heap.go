package clock

import "fmt"

// deterministicTimerHeap orders pending deterministic timers by deadline and
// then by creation sequence. It implements container/heap.Interface.
type deterministicTimerHeap []*deterministicTimer

func (h deterministicTimerHeap) Len() int { return len(h) }
func (h deterministicTimerHeap) Less(i, j int) bool {
	if h[i].deadlineElapsed != h[j].deadlineElapsed {
		return h[i].deadlineElapsed < h[j].deadlineElapsed
	}
	return h[i].sequence < h[j].sequence
}
func (h deterministicTimerHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *deterministicTimerHeap) Push(value any) {
	timer, ok := value.(*deterministicTimer)
	if !ok {
		panic(fmt.Sprintf("clock: timer heap received %T", value))
	}
	timer.index = len(*h)
	*h = append(*h, timer)
}
func (h *deterministicTimerHeap) Pop() any {
	old := *h
	n := len(old)
	timer := old[n-1]
	old[n-1] = nil
	timer.index = -1
	*h = old[:n-1]
	return timer
}
