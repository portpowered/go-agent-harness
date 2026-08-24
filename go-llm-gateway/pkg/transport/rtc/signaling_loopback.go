package rtc

import (
	"context"
	"strings"
	"sync"
	"time"
)

type loopbackExchange struct {
	mu         sync.Mutex
	done, wake chan struct{}
	timeout    time.Duration
	terminal   bool
	err        error
	messages   [2][]any
	read       [2]int
}
type LoopbackEndpoint struct {
	exchange *loopbackExchange
	index    int
}

// NewLoopbackSignalingPair returns an offerer and answerer without network resources; omitted configuration uses DefaultICEGatheringTimeout.
func NewLoopbackSignalingPair(config ...SignalingConfig) (*LoopbackEndpoint, *LoopbackEndpoint, error) {
	if len(config) > 1 {
		return nil, nil, ErrInvalidSignalingConfiguration
	}
	cfg := SignalingConfig{ICEGatheringTimeout: DefaultICEGatheringTimeout}
	if len(config) == 1 {
		cfg = config[0]
		if _, err := cfg.timeout(); err != nil {
			return nil, nil, err
		}
	}
	x := &loopbackExchange{timeout: cfg.ICEGatheringTimeout, done: make(chan struct{}), wake: make(chan struct{})}
	o, a := &LoopbackEndpoint{exchange: x}, &LoopbackEndpoint{exchange: x, index: 1}
	if cfg.Unreachable {
		x.end(ErrSignalingUnreachable)
	}
	return o, a, nil
}
func (x *loopbackExchange) end(err error) {
	if x.terminal {
		return
	}
	if err == nil {
		err = ErrSignalingClosed
	}
	x.terminal, x.err, x.messages = true, err, [2][]any{}
	close(x.done)
	close(x.wake)
}
func (x *loopbackExchange) notify()              { close(x.wake); x.wake = make(chan struct{}) }
func (x *loopbackExchange) opErr() error         { return x.err }
func (x *loopbackExchange) stop(err error) error { x.end(err); return x.opErr() }
func (x *loopbackExchange) fail(err error) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.terminal {
		x.end(err)
	}
	return x.opErr()
}
func (e *LoopbackEndpoint) Done() <-chan struct{} { return e.exchange.done }
func (e *LoopbackEndpoint) Close() error {
	e.exchange.mu.Lock()
	e.exchange.end(ErrSignalingClosed)
	e.exchange.mu.Unlock()
	return nil
}
func (e *LoopbackEndpoint) SendOffer(ctx context.Context, d SessionDescription) error {
	return e.send(ctx, d, 0, 0)
}
func (e *LoopbackEndpoint) SendAnswer(ctx context.Context, d SessionDescription) error {
	return e.send(ctx, d, 0, 1)
}
func (e *LoopbackEndpoint) send(ctx context.Context, value any, kind, owner int) error {
	if owner >= 0 && e.index != owner {
		return e.exchange.fail(ErrInvalidSignalingOrder)
	}
	if ctx != nil && ctx.Err() != nil {
		return e.exchange.fail(ctx.Err())
	}
	x := e.exchange
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := x.opErr(); err != nil {
		return err
	}
	if e.index == 1 && x.read[0] == 0 {
		return x.stop(ErrAnswerBeforeOffer)
	}
	messages := x.messages[e.index]
	if (kind == 0 && len(messages) > 0) || (kind > 0 && (len(messages) == 0 || messages[len(messages)-1] == nil)) {
		return x.stop(ErrInvalidSignalingOrder)
	}
	x.messages[e.index] = append(x.messages[e.index], value)
	x.notify()
	return nil
}

const (
	descriptionMode = iota
	candidateMode
	gatheringMode
)

func (e *LoopbackEndpoint) ReceiveOffer(ctx context.Context) (SessionDescription, error) {
	return await[SessionDescription](e, ctx, descriptionMode, "offer")
}
func (e *LoopbackEndpoint) ReceiveAnswer(ctx context.Context) (SessionDescription, error) {
	return await[SessionDescription](e, ctx, descriptionMode, "answer")
}
func (e *LoopbackEndpoint) SendCandidate(ctx context.Context, c ICECandidate) error {
	return e.send(ctx, c, 1, -1)
}
func (e *LoopbackEndpoint) ReceiveCandidate(ctx context.Context) (ICECandidate, error) {
	return await[ICECandidate](e, ctx, candidateMode, "")
}
func (e *LoopbackEndpoint) CompleteCandidateGathering(ctx context.Context) error {
	return e.send(ctx, nil, 2, -1)
}
func (e *LoopbackEndpoint) WaitCandidateGathering(ctx context.Context) error {
	_, err := await[struct{}](e, ctx, gatheringMode, "")
	return err
}
func await[T any](e *LoopbackEndpoint, ctx context.Context, mode int, want string) (T, error) {
	var zero T
	x, waitCtx := e.exchange, ctx
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	var cancel context.CancelFunc
	if mode != descriptionMode {
		waitCtx, cancel = context.WithTimeout(waitCtx, x.timeout)
		defer cancel()
	}
	for {
		v, ready, wake, err := x.next(e.index, mode, want)
		if ready {
			if v == nil {
				return zero, err
			}
			return v.(T), err
		}
		select {
		case <-x.done:
		case <-wake:
		case <-waitCtx.Done():
			if ctx != nil && ctx.Err() != nil {
				return zero, e.exchange.fail(ctx.Err())
			}
			if mode != descriptionMode {
				return zero, x.fail(ErrICEGatheringTimeout)
			}
			return zero, nil
		}
	}
}
func (x *loopbackExchange) next(i, mode int, want string) (any, bool, <-chan struct{}, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	wake := x.wake
	if x.terminal {
		if mode == gatheringMode && x.err == ErrSignalingClosed {
			return nil, true, wake, nil
		}
		return nil, true, wake, x.opErr()
	}
	if mode == descriptionMode {
		remote := 1 - i
		if (want == "answer" && len(x.messages[0]) == 0) || x.read[remote] != 0 {
			return nil, true, wake, x.stop(ErrInvalidSignalingOrder)
		}
		if messages := x.messages[remote]; len(messages) > 0 {
			got := messages[0].(SessionDescription)
			x.read[remote] = 1
			if !validDescription(got, want) {
				bad := ErrMalformedAnswer
				if want == "offer" {
					bad = ErrMalformedOffer
				}
				return SessionDescription{}, true, wake, x.stop(bad)
			}
			return got, true, wake, nil
		}
		return nil, false, wake, nil
	}
	if mode == candidateMode {
		remote, messages := 1-i, x.messages[1-i]
		if x.read[remote] == 0 {
			return nil, true, wake, x.stop(ErrInvalidSignalingOrder)
		}
		if x.read[remote] < len(messages)-1 {
			c := messages[x.read[remote]].(ICECandidate)
			x.read[remote]++
			return c, true, wake, nil
		}
		if len(messages) > 1 && messages[len(messages)-1] == nil {
			if len(messages) == 2 {
				return nil, true, wake, x.stop(ErrNoCandidates)
			}
			return ICECandidate{}, true, wake, ErrGatheringComplete
		}
		return nil, false, wake, nil
	}
	if len(x.messages[i]) < 2 || x.messages[i][len(x.messages[i])-1] != nil {
		return nil, false, wake, nil
	}
	if len(x.messages[i]) == 2 {
		return nil, true, wake, x.stop(ErrNoCandidates)
	}
	if !x.terminal && x.read[0] > 0 && x.read[1] > 0 && len(x.messages[0]) > 1 && len(x.messages[1]) > 1 && x.messages[0][len(x.messages[0])-1] == nil && x.messages[1][len(x.messages[1])-1] == nil && x.read[0] == len(x.messages[0])-1 && x.read[1] == len(x.messages[1])-1 {
		x.end(nil)
	}
	return nil, true, wake, nil
}
func validDescription(d SessionDescription, want string) bool {
	sdp := strings.TrimSpace(strings.ReplaceAll(d.SDP, "\r\n", "\n"))
	if d.Type != want || sdp == "" || strings.ContainsRune(sdp, '\r') || !strings.HasPrefix(sdp, "v=0\n") {
		return false
	}
	return strings.Contains(sdp, "\no=") && !strings.Contains(sdp, "\no=\n") && strings.Contains(sdp, "\ns=") && !strings.Contains(sdp, "\ns=\n") && strings.Contains(sdp, "\nt=") && !strings.Contains(sdp, "\nt=\n")
}
