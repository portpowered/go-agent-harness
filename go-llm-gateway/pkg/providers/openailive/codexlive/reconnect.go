package codexlive

import "time"

// Sideband reconnect defaults. After a lost sideband the transport waits as
// Codex does (codex-rs core/src/realtime_conversation/sideband.rs): 200 ms,
// doubling with each rapid loss up to 5 s, where a connection that stayed up
// for 30 s resets the count. It then dials up to five times, 200 ms apart and
// doubling, as OpenClaw connects its sideband
// (extensions/openai/realtime-quicksilver-sideband.ts) and as Codex retries a
// sideband join. A call the backend no longer knows (HTTP 404 or 410) or a
// rejected credential is never retried.
const (
	DefaultReconnectBaseDelay = 200 * time.Millisecond
	DefaultReconnectMaxDelay  = 5 * time.Second
	DefaultStableConnection   = 30 * time.Second
	DefaultDialAttempts       = 5
)

// reconnectPolicy times sideband reconnects. rapid counts losses since the
// last stable connection; only the reconnecting goroutine touches it.
type reconnectPolicy struct {
	base, max, stable time.Duration
	attempts          int
	rapid             int
}

func defaultReconnectPolicy() reconnectPolicy {
	return reconnectPolicy{
		base: DefaultReconnectBaseDelay, max: DefaultReconnectMaxDelay,
		stable: DefaultStableConnection, attempts: DefaultDialAttempts,
	}
}

// delayAfterLoss is the wait before redialing a sideband that was up for
// connectedFor.
func (p *reconnectPolicy) delayAfterLoss(connectedFor time.Duration) time.Duration {
	if connectedFor >= p.stable {
		p.rapid = 0
	}
	p.rapid++
	return p.backoff(p.rapid - 1)
}

// retryDelay is the wait before dial attempt n (n >= 1).
func (p *reconnectPolicy) retryDelay(attempt int) time.Duration {
	return p.backoff(attempt - 1)
}

// backoff is base doubled exponent times, capped at max.
func (p *reconnectPolicy) backoff(exponent int) time.Duration {
	delay := p.base
	for range exponent {
		if delay >= p.max {
			break
		}
		delay *= 2
	}
	return min(delay, p.max)
}
