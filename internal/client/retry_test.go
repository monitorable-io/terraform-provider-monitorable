package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

type recorder struct {
	mu     sync.Mutex
	slept  []time.Duration
	bodies []string
	keys   []string
	n      int
}

func (r *recorder) sleep(_ context.Context, d time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.slept = append(r.slept, d)
	return nil
}

func (r *recorder) total() (sum time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.slept {
		sum += d
	}
	return sum
}

// retryClient serves script[i] for request i (the last entry repeats).
type step struct {
	status     int
	retryAfter string
	body       string
}

func retryClient(t *testing.T, script ...step) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		i := rec.n
		rec.n++
		rec.bodies = append(rec.bodies, string(b))
		rec.keys = append(rec.keys, r.Header.Get("Idempotency-Key"))
		rec.mu.Unlock()
		if i >= len(script) {
			i = len(script) - 1
		}
		s := script[i]
		if s.status == -1 { // drop the connection
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		if s.retryAfter != "" {
			w.Header().Set("Retry-After", s.retryAfter)
		}
		body := s.body
		if body == "" {
			body = "{}"
		}
		if s.status >= 400 {
			w.Header().Set("Content-Type", "application/problem+json")
		}
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, testKey, testUA)
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = rec.sleep
	return c, rec
}

const problem429 = `{"title":"Too Many Requests","status":429,"code":"rate_limited","detail":"slow down","request_id":"r","class":"mutate"}`
const problem503 = `{"title":"Service Unavailable","status":503,"code":"internal","detail":"try later","request_id":"r"}`

func TestRetry429HonoursRetryAfter(t *testing.T) {
	c, rec := retryClient(t, step{429, "1", problem429}, step{200, "", "{}"})
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec.n != 2 {
		t.Fatalf("requests = %d, want 2", rec.n)
	}
	if rec.total() < time.Second {
		t.Fatalf("waited %v, want >= 1s", rec.total())
	}
}

func TestRetry429GivesUpAfter5(t *testing.T) {
	c, rec := retryClient(t, step{429, "1", problem429})
	_, err := c.Me(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 429 || ae.Code != "rate_limited" {
		t.Fatalf("err = %v", err)
	}
	if rec.n != 5 {
		t.Fatalf("requests = %d, want 5", rec.n)
	}
}

func TestRetry429TotalWaitCapped(t *testing.T) {
	c, rec := retryClient(t, step{429, "60", problem429})
	_, err := c.Me(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 429 {
		t.Fatalf("err = %v", err)
	}
	// 60 s + 60 s fit in the 120 s budget; a third wait would not.
	if rec.n != 3 {
		t.Fatalf("requests = %d, want 3", rec.n)
	}
	if rec.total() > 120*time.Second {
		t.Fatalf("waited %v, over the 120 s cap", rec.total())
	}
}

func TestRetry429WithoutRetryAfterWaitsOneSecond(t *testing.T) {
	c, rec := retryClient(t, step{429, "", problem429}, step{200, "", "{}"})
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.slept) != 1 || rec.slept[0] != time.Second {
		t.Fatalf("slept = %v", rec.slept)
	}
}

func TestRetry503ThenOK(t *testing.T) {
	c, rec := retryClient(t, step{503, "", problem503}, step{503, "", problem503}, step{200, "", "{}"})
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec.n != 3 {
		t.Fatalf("requests = %d, want 3", rec.n)
	}
	if len(rec.slept) != 2 || rec.slept[0] != time.Second || rec.slept[1] != 2*time.Second {
		t.Fatalf("backoff = %v, want [1s 2s]", rec.slept)
	}
}

func TestRetry503GivesUpAfter3(t *testing.T) {
	c, rec := retryClient(t, step{503, "", problem503})
	_, err := c.Me(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 503 {
		t.Fatalf("err = %v", err)
	}
	if rec.n != 3 {
		t.Fatalf("requests = %d, want 3", rec.n)
	}
}

func TestRetry503HonoursRetryAfter(t *testing.T) {
	c, rec := retryClient(t, step{503, "3", problem503}, step{200, "", "{}"})
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.slept) != 1 || rec.slept[0] != 3*time.Second {
		t.Fatalf("slept = %v, want [3s]", rec.slept)
	}
}

func TestRetry502And504(t *testing.T) {
	c, rec := retryClient(t, step{502, "", ""}, step{504, "", ""}, step{200, "", "{}"})
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec.n != 3 {
		t.Fatalf("requests = %d", rec.n)
	}
}

func TestRetryNetworkError(t *testing.T) {
	c, rec := retryClient(t, step{status: -1}, step{200, "", "{}"})
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec.n != 2 {
		t.Fatalf("requests = %d, want 2", rec.n)
	}
}

func TestRetryBudgetsAreSeparate(t *testing.T) {
	c, rec := retryClient(t,
		step{429, "1", problem429}, step{429, "1", problem429}, step{429, "1", problem429}, step{429, "1", problem429},
		step{503, "", problem503}, step{503, "", problem503}, step{200, "", "{}"})
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("4x429 + 2x503 must still succeed: %v", err)
	}
	if rec.n != 7 {
		t.Fatalf("requests = %d, want 7", rec.n)
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	c, rec := retryClient(t, step{400, "", `{"title":"Bad Request","status":400,"code":"invalid_argument","detail":"x","request_id":"r"}`})
	if _, err := c.Me(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if rec.n != 1 || len(rec.slept) != 0 {
		t.Fatalf("requests = %d slept = %v", rec.n, rec.slept)
	}
}

func TestRetryReplaysSameIdempotencyKey(t *testing.T) {
	created := `{"server":{"id":5,"name":"web"},"agent_api_key":"k","install_command":"c"}`
	c, rec := retryClient(t, step{503, "", problem503}, step{201, "", created})
	resp, err := c.CreateServer(context.Background(), "web", "idem-42")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Server.ID != 5 {
		t.Fatalf("resp = %+v", resp)
	}
	if len(rec.keys) != 2 || rec.keys[0] != "idem-42" || rec.keys[1] != "idem-42" {
		t.Fatalf("Idempotency-Keys = %v", rec.keys)
	}
	if rec.bodies[0] != `{"name":"web"}` || rec.bodies[1] != rec.bodies[0] {
		t.Fatalf("bodies = %q", rec.bodies)
	}
}

func TestRetryStopsWhenContextCancelled(t *testing.T) {
	c, rec := retryClient(t, step{503, "", problem503})
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	_, err := c.Me(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if rec.n != 1 {
		t.Fatalf("requests = %d, want 1", rec.n)
	}
}

func TestRetryIdempotencyInProgress(t *testing.T) {
	f := fake.New(fake.Options{})
	t.Cleanup(f.Close)
	c, err := New(f.URL(), testKey, testUA)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	c.sleep = rec.sleep
	f.InjectIdempotencyInProgress(1)

	resp, err := c.CreateServer(context.Background(), "web", "idem-busy")
	if err != nil {
		t.Fatal(err)
	}
	if resp.AgentAPIKey == "" || resp.Server.ID <= 0 {
		t.Fatalf("resp = %+v, want a first-time 201", resp)
	}
	if len(rec.slept) != 1 || rec.slept[0] != 2*time.Second {
		t.Fatalf("slept = %v, want [2s] (the Retry-After of the in-progress answer)", rec.slept)
	}
	var keys []string
	for _, r := range f.Requests() {
		if r.Method == "POST" && r.Path == "/v1/servers" {
			keys = append(keys, r.IdempotencyKey)
		}
	}
	if len(keys) != 2 || keys[0] != "idem-busy" || keys[1] != "idem-busy" {
		t.Fatalf("POST Idempotency-Keys = %v", keys)
	}
	if n := len(f.Servers()); n != 1 {
		t.Fatalf("servers = %d, want 1", n)
	}
}
