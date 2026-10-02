package fakelive

import (
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
)

// Step is one scripted action. Steps run in order on every connection,
// concurrently with the server's handling of client events. A step that
// waits stops when the connection ends.
type Step struct {
	name string
	run  func(*session) error
}

// Send writes server events in order. Sending a client-target
// session.delegation.created makes its id a known delegation for appends.
func Send(events ...openailive.Event) Step {
	return Step{name: "send", run: func(sess *session) error {
		for _, event := range events {
			if err := sess.write(event); err != nil {
				return err
			}
		}
		return nil
	}}
}

// SendRaw writes one frame exactly as given, for malformed or unmodelled
// server frames.
func SendRaw(messageType int, payload []byte) Step {
	owned := append([]byte(nil), payload...)
	return Step{name: "send raw", run: func(sess *session) error {
		return sess.writeRaw(messageType, owned)
	}}
}

// Wait pauses the script for d on the server's clock.
func Wait(d time.Duration) Step {
	return Step{name: "wait", run: func(sess *session) error {
		timer := sess.server.clock.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C():
			return nil
		case <-sess.done:
			return errSessionDone
		}
	}}
}

// AwaitStarted waits until the server has answered session.start with
// session.started.
func AwaitStarted() Step {
	return Step{name: "await started", run: func(sess *session) error {
		return sess.waitFor(func() bool { return sess.started })
	}}
}

// AwaitClient waits until the connection has received count client events
// of eventType in total.
func AwaitClient(eventType string, count int) Step {
	return Step{name: fmt.Sprintf("await %d %s", count, eventType), run: func(sess *session) error {
		return sess.waitFor(func() bool { return sess.counts[eventType] >= count })
	}}
}

// CloseSession sends session.closed with reason and the final usage, then
// closes the connection.
func CloseSession(reason string) Step {
	return Step{name: "close " + reason, run: func(sess *session) error {
		sess.closeWith(reason, "")
		return nil
	}}
}

// Drop closes the socket without session.closed.
func Drop() Step {
	return Step{name: "drop", run: func(sess *session) error {
		sess.finish()
		return nil
	}}
}
