package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/chromedp/chromedp"
)

// writeFixtureBody serves a fixture response. A failed write means the browser
// dropped the connection mid-response; the probe observes that through its own
// navigation or oracle result, so the handler has nothing further to report.
func writeFixtureBody(writer io.Writer, body []byte) {
	if _, err := writer.Write(body); err != nil {
		return
	}
}

// writeFixtureJSON serves a JSON fixture state with the same disconnect rule.
func writeFixtureJSON(writer io.Writer, value any) {
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		return
	}
}

// cancelProbeTarget closes the probe-owned temporary target on cleanup. The
// probe result is already decided and the launcher owns the browser, so a
// failed target close cannot change the reported outcome.
func cancelProbeTarget(target context.Context) {
	if err := chromedp.Cancel(target); err != nil {
		return
	}
}

// killProbeProcess stops a helper process during teardown. Kill fails only when
// the process already exited, which the caller observes through its done channel.
func killProbeProcess(process *os.Process) {
	if err := process.Kill(); err != nil {
		return
	}
}

const (
	crossProcessHTTPTimeout      = 2 * time.Second
	crossProcessOraclePoll       = 100 * time.Millisecond
	crossProcessTargetListLimit  = 1 << 20
	crossProcessOracleStateLimit = 64 << 10
)

// closeInto closes closer and records a close failure in *errp unless an
// earlier error is already being returned.
func closeInto(errp *error, closer io.Closer, what string) {
	if err := closer.Close(); err != nil && *errp == nil {
		*errp = fmt.Errorf("close %s: %w", what, err)
	}
}

// closeListenerInto closes a listener that a server may already have closed
// during shutdown; only unexpected close failures are recorded.
func closeListenerInto(errp *error, listener net.Listener) {
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) && *errp == nil {
		*errp = fmt.Errorf("close listener: %w", err)
	}
}

func readCrossProcessTarget(ctx context.Context, httpEndpoint, targetID string) (info crossProcessTargetInfo, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpEndpoint+"/json/list", nil)
	if err != nil {
		return crossProcessTargetInfo{}, fmt.Errorf("create target-list request: %w", err)
	}
	client := &http.Client{Timeout: crossProcessHTTPTimeout}
	response, err := client.Do(request)
	if err != nil {
		return crossProcessTargetInfo{}, fmt.Errorf("read target list: %w", err)
	}
	defer closeInto(&err, response.Body, "target list response body")
	if response.StatusCode != http.StatusOK {
		return crossProcessTargetInfo{}, fmt.Errorf("target list status = %s", response.Status)
	}
	var targets []crossProcessTargetInfo
	if err := json.NewDecoder(io.LimitReader(response.Body, crossProcessTargetListLimit)).Decode(&targets); err != nil {
		return crossProcessTargetInfo{}, fmt.Errorf("decode target list: %w", err)
	}
	for _, targetInfo := range targets {
		if targetInfo.ID == targetID {
			return targetInfo, nil
		}
	}
	return crossProcessTargetInfo{}, fmt.Errorf("target %s is not present", targetID)
}

func waitForHTTPOracle(ctx context.Context, stateURL string, predicate func(crossProcessPageState) bool) (crossProcessPageState, error) {
	ticker := time.NewTicker(crossProcessOraclePoll)
	defer ticker.Stop()
	var last crossProcessPageState
	var lastErr error
	for {
		err := pollHTTPOracle(ctx, stateURL, &last)
		if err == nil && (predicate == nil || predicate(last)) {
			return cloneCrossProcessPageState(last), nil
		}
		lastErr = err
		select {
		case <-ticker.C:
		case <-ctx.Done():
			if lastErr != nil {
				return crossProcessPageState{}, fmt.Errorf("wait for HTTP oracle: %w: %w (last state=%+v)", ctx.Err(), lastErr, last)
			}
			return crossProcessPageState{}, fmt.Errorf("wait for HTTP oracle: %w (last state=%+v)", ctx.Err(), last)
		}
	}
}

// pollHTTPOracle reads the fixture's page state once, decoding a 200
// response into last so the caller keeps the most recent observation.
func pollHTTPOracle(ctx context.Context, stateURL string, last *crossProcessPageState) (err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, stateURL, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: crossProcessHTTPTimeout}).Do(request)
	if err != nil {
		return err
	}
	defer closeInto(&err, response.Body, "oracle response body")
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("oracle status = %s", response.Status)
	}
	return json.NewDecoder(io.LimitReader(response.Body, crossProcessOracleStateLimit)).Decode(last)
}
