package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	maxTransientAttempts = 3                 // network errors, 502, 503, 504
	maxRateLimitAttempts = 5                 // 429
	maxRateLimitWait     = 120 * time.Second // total time spent waiting on 429s
	maxRetryAfter503     = 30 * time.Second  // ceiling for a server-chosen 503 delay
	defaultRetryDelay    = time.Second

	// maxInProgressWait is the total time spent waiting while the API answers
	// that a create with the same Idempotency-Key is still running. It is the
	// backend's claim TTL: after it the claim lapses and a retry either gets the
	// finished create's replay or runs the create itself.
	maxInProgressWait = 60 * time.Second
)

// inProgress reports whether resp is the API's "a request with this
// Idempotency-Key is still in progress" answer: a 503 idempotency_unavailable
// that carries Retry-After (the idempotency store being down sends none). It
// reads the body and puts it back, so the caller still sees it whole.
func inProgress(resp *http.Response) bool {
	if resp.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	if _, ok := retryAfter(resp.Header); !ok {
		return false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(b))
	if err != nil {
		return false
	}
	var p struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(b, &p) == nil && p.Code == "idempotency_unavailable"
}

// sleepContext waits for d or until ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryAfter reads a Retry-After header holding integer seconds.
func retryAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

// backoff is the delay before the next attempt after the n-th transient
// failure: 1 s, 2 s, 4 s.
func backoff(n int) time.Duration { return defaultRetryDelay << (n - 1) }

// do sends req with retries and returns the final response, whatever its
// status; the caller closes the body. Transient failures (network errors,
// 502/503/504) have an attempt budget of 3 and 429 a separate budget of 5 and
// 120 s of total waiting. The API's "create still in progress" 503 has its own
// budget of 60 s of total waiting, so a slow create is waited out rather than
// abandoned while it runs. The body is re-created for every attempt, so a
// retried POST carries the same bytes and the same Idempotency-Key.
func (c *Client) do(ctx context.Context, req *http.Request) (*http.Response, error) {
	var transient, limited int
	var waited, waitedInProgress time.Duration
	for {
		r := req.Clone(ctx)
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r.Body = body
		}
		resp, err := c.http.Do(r)

		var delay time.Duration
		var reason string
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			transient++
			if transient >= maxTransientAttempts {
				return nil, err
			}
			delay, reason = backoff(transient), "network error"
		case resp.StatusCode == http.StatusTooManyRequests:
			limited++
			if limited >= maxRateLimitAttempts {
				return resp, nil
			}
			delay = defaultRetryDelay
			if d, ok := retryAfter(resp.Header); ok {
				delay = d
			}
			if waited+delay > maxRateLimitWait {
				return resp, nil
			}
			waited += delay
			reason = "429"
		case inProgress(resp):
			delay, _ = retryAfter(resp.Header)
			delay = min(max(delay, defaultRetryDelay), maxRetryAfter503) // Retry-After: 0 must not spin
			if waitedInProgress+delay > maxInProgressWait {
				return resp, nil
			}
			waitedInProgress += delay
			reason = "idempotency key in progress"
		case resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout:
			transient++
			if transient >= maxTransientAttempts {
				return resp, nil
			}
			delay = backoff(transient)
			if d, ok := retryAfter(resp.Header); ok {
				delay = min(d, maxRetryAfter503)
			}
			reason = strconv.Itoa(resp.StatusCode)
		default:
			return resp, nil
		}

		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
		}
		// Method and path only: no headers, no query string, no body.
		tflog.Debug(ctx, "retrying request", map[string]any{
			"method": req.Method, "path": req.URL.Path, "reason": reason, "delay_ms": delay.Milliseconds(),
		})
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}
