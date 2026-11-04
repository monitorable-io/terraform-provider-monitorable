package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflogtest"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

const (
	testKey = "mon_test_fake1"
	testUA  = "terraform-provider-monitorable/test (+https://monitorable.io/docs/api/terraform/)"
)

// newTestClient returns a client against f whose sleeper never sleeps.
func newTestClient(t *testing.T, f *fake.Fake) *Client {
	t.Helper()
	c, err := New(f.URL(), testKey, testUA)
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func newFake(t *testing.T, o fake.Options) *fake.Fake {
	t.Helper()
	f := fake.New(o)
	t.Cleanup(f.Close)
	return f
}

func TestNewRequiresKeyAndNormalisesEndpoint(t *testing.T) {
	if _, err := New("https://api.monitorable.io", "", testUA); err == nil {
		t.Fatal("empty api key must be an error")
	}
	if _, err := New("", testKey, testUA); err == nil {
		t.Fatal("empty endpoint must be an error")
	}
	if _, err := New("api.monitorable.io", testKey, testUA); err == nil {
		t.Fatal("an endpoint without a scheme must be an error")
	}
	for _, bad := range []string{
		"https://api.monitorable.io/v1", "https://api.monitorable.io/v1/", "https://api.monitorable.io?x=1",
		"https://api.monitorable.io/?x=1", "https://api.monitorable.io#frag", "https://api.monitorable.io?",
		"ftp://api.monitorable.io", "https://user:pw@api.monitorable.io",
	} {
		_, err := New(bad, testKey, testUA)
		if err == nil {
			t.Errorf("New(%q) must be an error", bad)
			continue
		}
		if strings.Contains(err.Error(), "pw") {
			t.Errorf("error for %q echoes the credentials: %v", bad, err)
		}
	}
	_, err := New("https://api.monitorable.io/v1", testKey, testUA)
	if want := `endpoint must be scheme://host[:port] without a path, got "https://api.monitorable.io/v1"`; err == nil || err.Error() != want {
		t.Errorf("path error = %v, want %q", err, want)
	}
	if c, err := New("http://127.0.0.1:8080", testKey, testUA); err != nil || c.endpoint != "http://127.0.0.1:8080" {
		t.Fatalf("host:port endpoint: %v %v", c, err)
	}
	c, err := New("https://api.monitorable.io/", testKey, testUA)
	if err != nil {
		t.Fatal(err)
	}
	if c.endpoint != "https://api.monitorable.io" {
		t.Fatalf("endpoint = %q", c.endpoint)
	}
	if c.http.Timeout != 30*time.Second {
		t.Fatalf("per-attempt timeout = %v, want 30s", c.http.Timeout)
	}
}

func TestMeSendsBearerAndUserAgent(t *testing.T) {
	var gotAuth, gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA, gotAccept = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("Accept")
		_, _ = w.Write([]byte(`{"key_id":7,"key_name":"ci","scopes":["servers:read"],"role":"admin","organization_id":3,"organization_name":"Acme","plan_name":"Pro","expires_at":null}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, testKey, testUA)
	if err != nil {
		t.Fatal(err)
	}
	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+testKey {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if !strings.HasPrefix(gotUA, "terraform-provider-monitorable/") {
		t.Fatalf("User-Agent = %q", gotUA)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q", gotAccept)
	}
	if me.KeyID != 7 || me.OrganizationID != 3 || me.OrganizationName != "Acme" || me.PlanName != "Pro" ||
		me.Role != "admin" || len(me.Scopes) != 1 || me.ExpiresAt != nil {
		t.Fatalf("me = %+v", me)
	}
}

func TestCreateServerSendsIdempotencyKeyAnd201(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	resp, err := c.CreateServer(context.Background(), "web-1", "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.AgentAPIKey == "" || resp.InstallCommand == "" || resp.Recovery != "" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Server.ID <= 0 || resp.Server.Name != "web-1" || resp.Server.CreatedAt == nil || resp.Server.LastSeenAt != nil {
		t.Fatalf("server = %+v", resp.Server)
	}
	reqs := f.Requests()
	if len(reqs) != 1 || reqs[0].IdempotencyKey != "idem-1" || reqs[0].Method != "POST" || reqs[0].Path != "/v1/servers" {
		t.Fatalf("requests = %+v", reqs)
	}
	if reqs[0].Body != `{"name":"web-1"}` {
		t.Fatalf("body = %q", reqs[0].Body)
	}
}

func TestCreateServerReplayHasNoKey(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	first, err := c.CreateServer(context.Background(), "web-1", "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.CreateServer(context.Background(), "web-1", "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Server.ID != first.Server.ID {
		t.Fatalf("replay id %d != %d", again.Server.ID, first.Server.ID)
	}
	if again.AgentAPIKey != "" || again.InstallCommand != "" {
		t.Fatalf("replay carries a key: %+v", again)
	}
	if want := fmt.Sprintf("POST /v1/servers/%d/agent-key:rotate", first.Server.ID); again.Recovery != want {
		t.Fatalf("recovery = %q, want %q", again.Recovery, want)
	}
}

func TestCreateServerLostResponseReplays(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	f.LoseNextCreateResponse()
	resp, err := c.CreateServer(context.Background(), "web-1", "idem-lost")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.Servers()); n != 1 {
		t.Fatalf("servers = %d, want 1", n)
	}
	if resp.AgentAPIKey != "" || resp.Recovery == "" {
		t.Fatalf("resp = %+v, want no key and a recovery", resp)
	}
	var keys []string
	for _, r := range f.Requests() {
		if r.Method == "POST" && r.Path == "/v1/servers" {
			keys = append(keys, r.IdempotencyKey)
		}
	}
	if len(keys) != 2 || keys[0] != "idem-lost" || keys[1] != "idem-lost" {
		t.Fatalf("POST Idempotency-Keys = %v", keys)
	}
}

func TestCreateServerPlanLimit402(t *testing.T) {
	f := newFake(t, fake.Options{MaxServers: 1})
	c := newTestClient(t, f)
	if _, err := c.CreateServer(context.Background(), "a", "k1"); err != nil {
		t.Fatal(err)
	}
	_, err := c.CreateServer(context.Background(), "b", "k2")
	if !IsPlanLimit(err) {
		t.Fatalf("err = %v, want plan limit", err)
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 402 {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(err.Error(), "plan_limit_reached") {
		t.Fatalf("Error() = %q", err.Error())
	}
	if IsNotFound(err) {
		t.Fatal("402 is not a not-found")
	}
}

func TestGetServerNotFound(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	_, err := c.GetServer(context.Background(), 999)
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want not found", err)
	}
	if IsNotFound(errors.New("x")) || IsNotFound(nil) || IsPlanLimit(nil) {
		t.Fatal("helpers must be false for foreign / nil errors")
	}
}

func TestUpdateServer(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	id := f.AddServer("old")
	s, err := c.UpdateServer(context.Background(), id, "new")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != id || s.Name != "new" || f.Servers()[0].Name != "new" {
		t.Fatalf("server = %+v", s)
	}
	if _, err := c.UpdateServer(context.Background(), 999, "x"); !IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteServer404IsNil(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	id := f.AddServer("a")
	if err := c.DeleteServer(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(f.Servers()) != 0 {
		t.Fatal("not deleted")
	}
	if err := c.DeleteServer(context.Background(), id); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestListServersFollowsCursor(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	for i := 0; i < 150; i++ {
		f.AddServer(fmt.Sprintf("srv-%03d", i))
	}
	got, err := c.ListServers(context.Background(), ListServersParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 150 {
		t.Fatalf("rows = %d, want 150", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Fatal("rows are not ordered by id / contain duplicates")
		}
	}
	var pages []string
	for _, r := range f.Requests() {
		if r.Path == "/v1/servers" && r.Method == "GET" {
			pages = append(pages, r.Query)
		}
	}
	if len(pages) != 2 {
		t.Fatalf("requests = %v, want 2", pages)
	}
	if !strings.Contains(pages[0], "limit=100") || strings.Contains(pages[0], "cursor=") {
		t.Fatalf("first page query = %q", pages[0])
	}
	if !strings.Contains(pages[1], "cursor=") {
		t.Fatalf("second page query = %q lacks cursor", pages[1])
	}
}

func TestListServersStatusAndQuery(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	f.AddServer("Web-One")
	f.AddServer("db-1")
	got, err := c.ListServers(context.Background(), ListServersParams{Status: "offline", Query: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Web-One" {
		t.Fatalf("got = %+v", got)
	}
	// A server nothing has reported on is offline (until it is a week old), not dormant.
	if got[0].Status != "offline" || got[0].Triage != "unreachable" {
		t.Fatalf("new server status/triage = %q/%q, want offline/unreachable", got[0].Status, got[0].Triage)
	}
	q := f.Requests()[0].Query
	if !strings.Contains(q, "status=offline") || !strings.Contains(q, "q=web") {
		t.Fatalf("query = %q", q)
	}
	if none, err := c.ListServers(context.Background(), ListServersParams{Status: "dormant"}); err != nil || len(none) != 0 {
		t.Fatalf("dormant filter = %+v, %v; want none", none, err)
	}
	if _, err := c.ListServers(context.Background(), ListServersParams{Status: "bogus"}); err == nil {
		t.Fatal("bogus status must be a 400")
	}
}

func TestListServersStopsOnStuckCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":1,"name":"a"}],"next_cursor":"same"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testKey, testUA)
	if _, err := c.ListServers(context.Background(), ListServersParams{}); err == nil {
		t.Fatal("a cursor that does not advance must be an error, not a loop")
	}
}

func TestListAlertTemplatesAndChannels(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	tpls, err := c.ListAlertTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tpls) != 2 || tpls[0].ID != 1 || tpls[0].Name != "Default" || !tpls[0].IsDefault || tpls[1].ID != 2 || tpls[1].IsDefault {
		t.Fatalf("templates = %+v", tpls)
	}
	chs, err := c.ListNotificationChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 2 || chs[0].Type != "email" || chs[0].Name != "Ops" || !chs[0].Enabled || chs[1].Type != "slack" {
		t.Fatalf("channels = %+v", chs)
	}
}

func TestSetAlertTemplateAndReadBack(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	ctx := context.Background()
	id := f.AddServer("a")
	if err := c.SetServerAlertTemplate(ctx, id, 2); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.GetServer(ctx, id); s == nil || s.AlertTemplateID != 2 {
		t.Fatalf("after set 2: %+v", s)
	}
	if err := c.SetServerAlertTemplate(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.GetServer(ctx, id); s == nil || s.AlertTemplateID != 0 {
		t.Fatalf("after set 0: %+v", s)
	}
	if err := c.SetServerAlertTemplate(ctx, id, 77); !IsNotFound(err) {
		t.Fatalf("unknown template: %v", err)
	}
}

func TestMuteUnmuteRoundTrip(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	ctx := context.Background()
	id := f.AddServer("a")
	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	st, err := c.MuteServerAlerts(ctx, id, &until, "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Muted || st.ServerID != id || st.Until == nil || !st.Until.Equal(until) || st.Reason != "maintenance" {
		t.Fatalf("state = %+v", st)
	}
	s, _ := c.GetServer(ctx, id)
	if !s.AlertsMuted || s.AlertsMutedUntil == nil || !s.AlertsMutedUntil.Equal(until) || s.AlertsMutedReason != "maintenance" {
		t.Fatalf("server = %+v", s)
	}

	// An indefinite mute sends no "until" at all.
	if st, err = c.MuteServerAlerts(ctx, id, nil, ""); err != nil || !st.Muted || st.Until != nil {
		t.Fatalf("indefinite mute = %+v, %v", st, err)
	}
	var body string
	for _, r := range f.Requests() {
		if strings.HasSuffix(r.Path, ":mute") {
			body = r.Body
		}
	}
	if strings.Contains(body, "until") || strings.Contains(body, "reason") {
		t.Fatalf("indefinite mute body = %q, want neither field", body)
	}

	if err := c.UnmuteServerAlerts(ctx, id); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.GetServer(ctx, id); s.AlertsMuted {
		t.Fatal("still muted after unmute")
	}
	if err := c.UnmuteServerAlerts(ctx, id); err != nil {
		t.Fatalf("second unmute: %v", err)
	}
	if err := c.UnmuteServerAlerts(ctx, 999); err != nil {
		t.Fatalf("unmute of a missing server must be nil, got %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := c.MuteServerAlerts(ctx, id, &past, ""); err == nil {
		t.Fatal("until in the past must be refused")
	}
}

func TestAPIErrorFromProblem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"type":"https://api.monitorable.io/problems/insufficient_scope","title":"Forbidden","status":403,"detail":"requires scope servers:write","code":"insufficient_scope","request_id":"r1"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testKey, testUA)
	_, err := c.CreateServer(context.Background(), "x", "k")
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v", err)
	}
	if ae.Status != 403 || ae.Code != "insufficient_scope" || ae.Title != "Forbidden" ||
		ae.Detail != "requires scope servers:write" || ae.RequestID != "r1" {
		t.Fatalf("ae = %+v", ae)
	}
	if want := "Forbidden (insufficient_scope): requires scope servers:write [request_id r1]"; ae.Error() != want {
		t.Fatalf("Error() = %q, want %q", ae.Error(), want)
	}
	if ae.IsNotFound() || IsNotFound(err) || IsPlanLimit(err) {
		t.Fatal("a 403 is neither not-found nor plan-limit")
	}
}

func TestNonProblemBodyStillErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html><body>bad gateway from the proxy</body></html>"))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testKey, testUA)
	c.sleep = func(context.Context, time.Duration) error { return nil }
	_, err := c.Me(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v", err)
	}
	if ae.Status != 502 || ae.Code != "http_502" || ae.Detail != "Bad Gateway" {
		t.Fatalf("ae = %+v", ae)
	}
	if strings.Contains(err.Error(), "proxy") {
		t.Fatalf("Error() leaks the upstream body: %q", err.Error())
	}
}

func TestBearerNeverInErrorOrLog(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	ctx := tflogtestContext(&logBuf)

	var errs []string
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	// Real fake: failures of every kind, including retried ones.
	f := newFake(t, fake.Options{MaxServers: 1, Scopes: []string{"servers:read", "servers:write"}})
	c := newTestClient(t, f)
	_, err := c.CreateServer(ctx, "a", "k1")
	collect(err)
	_, err = c.CreateServer(ctx, "b", "k2")
	collect(err)
	_, err = c.GetServer(ctx, 999)
	collect(err)
	_, err = c.ListAlertTemplates(ctx)
	collect(err) // 403 insufficient_scope
	f.Inject503(2)
	_, err = c.Me(ctx)
	collect(err)
	f.Inject429(5)
	_, err = c.Me(ctx)
	collect(err)

	// A hostile upstream that echoes the Authorization header into the problem.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(401)
		b, _ := json.Marshal(map[string]any{
			"title": "Unauthorized", "status": 401, "code": "invalid_api_key",
			"detail": "bad credentials: " + r.Header.Get("Authorization"), "request_id": "r9",
		})
		_, _ = w.Write(b)
	}))
	defer echo.Close()
	ec, _ := New(echo.URL, testKey, testUA)
	_, err = ec.Me(ctx)
	collect(err)

	// A dead endpoint (network error path).
	dead := httptest.NewServer(http.NotFoundHandler())
	dc, _ := New(dead.URL, testKey, testUA)
	dc.sleep = func(context.Context, time.Duration) error { return nil }
	dead.Close()
	_, err = dc.Me(ctx)
	collect(err)

	if len(errs) < 6 {
		t.Fatalf("only %d errors collected: %v", len(errs), errs)
	}
	for _, e := range errs {
		if strings.Contains(e, "mon_test_fake") {
			t.Errorf("error string holds the bearer: %q", e)
		}
	}
	if strings.Contains(logBuf.String(), "mon_test_fake") {
		t.Errorf("log output holds the bearer:\n%s", logBuf.String())
	}
	if b, _ := json.Marshal(f.Requests()); strings.Contains(string(b), "mon_test_fake") {
		t.Errorf("fake recordings hold the bearer: %s", b)
	}
	if !strings.Contains(logBuf.String(), "retrying request") {
		t.Errorf("expected retry log lines in the captured log; got:\n%s", logBuf.String())
	}
}

func tflogtestContext(w *bytes.Buffer) context.Context {
	return tflogtest.RootLogger(context.Background(), w)
}

func TestCreateServerRequiresIdempotencyKey(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	for _, key := range []string{"", strings.Repeat("k", 65)} {
		if resp, err := c.CreateServer(context.Background(), "a", key); err == nil || resp != nil {
			t.Fatalf("key of %d characters: resp = %+v, err = %v; want an error", len(key), resp, err)
		}
	}
	if n := len(f.Requests()); n != 0 {
		t.Fatalf("%d requests were sent; an unsafe create must not leave the client", n)
	}
	if _, err := c.CreateServer(context.Background(), "a", strings.Repeat("k", 64)); err != nil {
		t.Fatalf("a 64 character key is valid: %v", err)
	}
}

func TestClientFormattingNeverPrintsKey(t *testing.T) {
	c, err := New("https://api.monitorable.io", testKey, testUA)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []any{c, *c} {
		out := fmt.Sprintf("%v %+v %#v %s", v, v, v, v)
		if strings.Contains(out, "mon_test_fake") {
			t.Errorf("formatting %T prints the key: %s", v, out)
		}
		if !strings.Contains(out, "api.monitorable.io") {
			t.Errorf("formatting %T lost the endpoint: %s", v, out)
		}
	}
}

func TestNotFoundNeedsAnAPIProblem(t *testing.T) {
	if !(&APIError{Status: 404, Code: "not_found"}).IsNotFound() {
		t.Fatal("404 not_found is not found")
	}
	proxy := &APIError{Status: 404, Code: "http_404"}
	if proxy.IsNotFound() || IsNotFound(proxy) {
		t.Fatal("a 404 without a problem body (code http_404) must not count as gone")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(404)
		_, _ = w.Write([]byte("<html>edge: no route</html>"))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, testKey, testUA)
	ctx := context.Background()
	if err := c.DeleteServer(ctx, 5); err == nil || IsNotFound(err) {
		t.Fatalf("DeleteServer on an edge 404 = %v, want a non-not-found error", err)
	}
	if err := c.UnmuteServerAlerts(ctx, 5); err == nil || IsNotFound(err) {
		t.Fatalf("UnmuteServerAlerts on an edge 404 = %v, want a non-not-found error", err)
	}
	var ae *APIError
	if _, err := c.GetServer(ctx, 5); !errors.As(err, &ae) || ae.Code != "http_404" || IsNotFound(err) {
		t.Fatalf("GetServer on an edge 404 = %v", err)
	}
}

func TestRedirectsAreRefused(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the redirect target received a request")
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/me", http.StatusFound)
	}))
	defer src.Close()
	c, _ := New(src.URL, testKey, testUA)
	_, err := c.Me(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 302 || ae.Code != "http_302" {
		t.Fatalf("err = %#v, want APIError http_302", err)
	}
}

func TestPlaintextHost(t *testing.T) {
	cases := map[string]string{ // endpoint -> host warned about ("" = no warning)
		"https://api.monitorable.io":   "",
		"https://api.example.com:8443": "",
		"http://localhost:8080":        "",
		"http://LOCALHOST":             "",
		"http://127.0.0.1:9000":        "",
		"http://127.10.0.1":            "",
		"http://[::1]:9000":            "",
		"http://api.monitorable.lan":   "",
		"http://box.local:8080":        "",
		"http://api.example.com":       "api.example.com",
		"http://api.example.com:8080":  "api.example.com:8080",
		"http://10.0.0.5":              "10.0.0.5",
		"http://[2001:db8::1]:80":      "[2001:db8::1]:80",
		"http://localhost.example.com": "localhost.example.com",
		"http://example.lan.com":       "example.lan.com",
	}
	for endpoint, want := range cases {
		c, err := New(endpoint, testKey, testUA)
		if err != nil {
			t.Fatalf("New(%q): %v", endpoint, err)
		}
		host, plaintext := c.PlaintextHost()
		if plaintext != (want != "") || host != want {
			t.Errorf("PlaintextHost(%q) = %q, %v; want %q, %v", endpoint, host, plaintext, want, want != "")
		}
	}
}
