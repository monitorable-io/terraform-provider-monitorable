package fake_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

const testKey = "mon_test_fake_unit"

func call(t *testing.T, f *fake.Fake, method, path, body string, hdr map[string]string) (int, http.Header, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.URL()+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: body is not JSON: %q", method, path, raw)
		}
	}
	return resp.StatusCode, resp.Header, out
}

func newFake(t *testing.T, o fake.Options) *fake.Fake {
	t.Helper()
	f := fake.New(o)
	t.Cleanup(f.Close)
	return f
}

func TestCreateThenReplay(t *testing.T) {
	f := newFake(t, fake.Options{})
	h := map[string]string{"Idempotency-Key": "k1"}

	st, hdr, first := call(t, f, "POST", "/v1/servers", `{"name":"web-1"}`, h)
	if st != 201 {
		t.Fatalf("first create = %d, want 201", st)
	}
	key, _ := first["agent_api_key"].(string)
	if !strings.HasPrefix(key, "mon_agent_fake_") {
		t.Fatalf("agent_api_key = %q", first["agent_api_key"])
	}
	cmd, _ := first["install_command"].(string)
	if cmd != "curl -fsSL https://get.monitorable.net/install.sh | sudo sh -s -- --api-key="+key {
		t.Fatalf("install_command = %q", cmd)
	}
	if _, ok := first["recovery"]; ok {
		t.Fatalf("first response carries recovery: %v", first["recovery"])
	}
	if hdr.Get("X-Request-Id") == "" || hdr.Get("RateLimit-Limit") == "" ||
		hdr.Get("RateLimit-Remaining") == "" || hdr.Get("RateLimit-Reset") == "" {
		t.Fatalf("missing request id / rate limit headers: %v", hdr)
	}
	id := first["server"].(map[string]any)["id"].(float64)

	st, _, replay := call(t, f, "POST", "/v1/servers", `{"name":"web-1"}`, h)
	if st != 200 {
		t.Fatalf("replay = %d, want 200", st)
	}
	if _, ok := replay["agent_api_key"]; ok {
		t.Fatal("replay carries agent_api_key (must be absent)")
	}
	if _, ok := replay["install_command"]; ok {
		t.Fatal("replay carries install_command (must be absent)")
	}
	if replay["server"].(map[string]any)["id"].(float64) != id {
		t.Fatal("replay returned a different server")
	}
	if got, want := replay["recovery"], "POST /v1/servers/"+itoa(id)+"/agent-key:rotate"; got != want {
		t.Fatalf("recovery = %v, want %q", got, want)
	}
	if n := len(f.Servers()); n != 1 {
		t.Fatalf("servers = %d, want 1", n)
	}

	st, _, prob := call(t, f, "POST", "/v1/servers", `{"name":"other"}`, h)
	if st != 422 || prob["code"] != "idempotency_key_reused" {
		t.Fatalf("different body = %d %v, want 422 idempotency_key_reused", st, prob)
	}
}

func itoa(v float64) string { return strconv.FormatInt(int64(v), 10) }

func TestPlanLimit402(t *testing.T) {
	f := newFake(t, fake.Options{MaxServers: 1})
	if st, _, _ := call(t, f, "POST", "/v1/servers", `{"name":"a"}`, nil); st != 201 {
		t.Fatalf("first = %d", st)
	}
	st, hdr, prob := call(t, f, "POST", "/v1/servers", `{"name":"b"}`, nil)
	if st != 402 || prob["code"] != "plan_limit_reached" || prob["detail"] != "max_servers is 1 on your plan" {
		t.Fatalf("second = %d %v", st, prob)
	}
	if prob["title"] != "Plan limit reached" || prob["type"] != "https://monitorable.io/docs/api/errors#plan_limit_reached" {
		t.Fatalf("title/type = %v / %v, want the backend's", prob["title"], prob["type"])
	}
	if !strings.HasPrefix(hdr.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("content type %q", hdr.Get("Content-Type"))
	}
	for _, k := range []string{"type", "title", "status", "detail", "code", "request_id"} {
		if _, ok := prob[k]; !ok {
			t.Fatalf("problem lacks %q: %v", k, prob)
		}
	}
	if len(f.Servers()) != 1 {
		t.Fatal("limit exceeded server was created")
	}
}

func TestAuthScopesAndRole(t *testing.T) {
	f := newFake(t, fake.Options{Scopes: []string{"servers:read"}, Role: "member"})
	req, _ := http.NewRequest("GET", f.URL()+"/v1/servers", nil)
	req.Header.Set("Authorization", "Bearer something-else")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("foreign key = %d, want 401", resp.StatusCode)
	}
	if st, _, _ := call(t, f, "GET", "/v1/servers", "", nil); st != 200 {
		t.Fatalf("read with scope = %d", st)
	}
	st, _, p := call(t, f, "POST", "/v1/servers", `{"name":"x"}`, nil)
	if st != 403 || p["code"] != "insufficient_scope" {
		t.Fatalf("write without scope = %d %v", st, p)
	}
	st, _, p = call(t, f, "GET", "/v1/alert-templates", "", nil)
	if st != 403 || p["code"] != "insufficient_scope" {
		t.Fatalf("alerts:read missing = %d %v", st, p)
	}

	g := newFake(t, fake.Options{Role: "member"})
	st, _, p = call(t, g, "POST", "/v1/servers", `{"name":"x"}`, nil)
	if st != 403 || p["code"] != "insufficient_role" {
		t.Fatalf("member write = %d %v", st, p)
	}
}

func TestValidationAndNotFound(t *testing.T) {
	f := newFake(t, fake.Options{})
	for _, body := range []string{`{"name":""}`, `{"name":"` + strings.Repeat("a", 101) + `"}`} {
		st, _, p := call(t, f, "POST", "/v1/servers", body, nil)
		if st != 400 || p["code"] != "invalid_argument" {
			t.Fatalf("create %.20s = %d %v", body, st, p)
		}
	}
	if st, _, p := call(t, f, "GET", "/v1/servers/999", "", nil); st != 404 || p["code"] != "not_found" {
		t.Fatalf("get unknown = %d %v", st, p)
	}
	if st, _, _ := call(t, f, "DELETE", "/v1/servers/999", "", nil); st != 404 {
		t.Fatalf("delete unknown = %d", st)
	}
	id := f.AddServer("a")
	path := "/v1/servers/" + itoa(float64(id))
	if st, _, _ := call(t, f, "DELETE", path, "", nil); st != 204 {
		t.Fatalf("delete = %d", st)
	}
	if st, _, _ := call(t, f, "GET", path, "", nil); st != 404 {
		t.Fatalf("get after delete = %d", st)
	}
}

func TestMuteAndTemplateRules(t *testing.T) {
	f := newFake(t, fake.Options{})
	id := f.AddServer("a")
	base := "/v1/servers/" + itoa(float64(id))

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if st, _, p := call(t, f, "POST", base+"/alerts:mute", `{"until":"`+past+`"}`, nil); st != 400 || p["code"] != "invalid_argument" {
		t.Fatalf("past until = %d %v", st, p)
	}
	far := time.Now().Add(400 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if st, _, _ := call(t, f, "POST", base+"/alerts:mute", `{"until":"`+far+`"}`, nil); st != 400 {
		t.Fatalf("until > 366 days = %d", st)
	}
	if st, _, _ := call(t, f, "POST", base+"/alerts:mute", `{"reason":"`+strings.Repeat("r", 201)+`"}`, nil); st != 400 {
		t.Fatalf("long reason = %d", st)
	}
	st, _, m := call(t, f, "POST", base+"/alerts:mute", `{"reason":"maintenance"}`, nil)
	if st != 200 || m["muted"] != true || m["until"] != nil || m["reason"] != "maintenance" {
		t.Fatalf("indefinite mute = %d %v", st, m)
	}
	// Unmuting a server that is not muted is still 200.
	call(t, f, "POST", base+"/alerts:unmute", "", nil)
	st, _, m = call(t, f, "POST", base+"/alerts:unmute", "", nil)
	if st != 200 || m["muted"] != false {
		t.Fatalf("second unmute = %d %v", st, m)
	}
	if st, _, _ := call(t, f, "POST", "/v1/servers/999/alerts:unmute", "", nil); st != 404 {
		t.Fatalf("unmute unknown = %d", st)
	}

	if st, _, p := call(t, f, "PUT", base+"/alert-template", `{"template_id":77}`, nil); st != 404 || p["detail"] != "alert template not found" {
		t.Fatalf("unknown template = %d %v", st, p)
	}
	if st, _, _ := call(t, f, "PUT", base+"/alert-template", `{"template_id":-1}`, nil); st != 400 {
		t.Fatalf("negative template = %d", st)
	}
	if st, _, _ := call(t, f, "PUT", base+"/alert-template", `{"template_id":2}`, nil); st != 200 {
		t.Fatalf("set template = %d", st)
	}
	if st, _, _ := call(t, f, "PUT", base+"/alert-template", `{"template_id":0}`, nil); st != 200 {
		t.Fatalf("reset template = %d", st)
	}
}

func TestSeededListsAndMe(t *testing.T) {
	f := newFake(t, fake.Options{})
	_, _, tpl := call(t, f, "GET", "/v1/alert-templates", "", nil)
	data := tpl["data"].([]any)
	if len(data) != 2 || data[0].(map[string]any)["is_default"] != true || data[1].(map[string]any)["name"] != "Strict" {
		t.Fatalf("templates = %v", tpl)
	}
	_, _, ch := call(t, f, "GET", "/v1/notification-channels", "", nil)
	cd := ch["data"].([]any)
	if len(cd) != 2 || cd[0].(map[string]any)["type"] != "email" || cd[1].(map[string]any)["type"] != "slack" {
		t.Fatalf("channels = %v", ch)
	}
	_, _, me := call(t, f, "GET", "/v1/me", "", nil)
	if me["role"] != "admin" {
		t.Fatalf("me = %v", me)
	}
}

func TestInjectionAndLostResponse(t *testing.T) {
	f := newFake(t, fake.Options{})

	f.InjectStatusOn("POST", "/v1/servers", 503, 1)
	if st, _, _ := call(t, f, "GET", "/v1/me", "", nil); st != 200 {
		t.Fatalf("unrelated request consumed the injection: %d", st)
	}
	st, _, p := call(t, f, "POST", "/v1/servers", `{"name":"a"}`, nil)
	if st != 503 || p["code"] != "internal" {
		t.Fatalf("targeted injection = %d %v", st, p)
	}
	if len(f.Servers()) != 0 {
		t.Fatal("injected failure still created a server")
	}

	f.Inject429(1)
	st, hdr, p := call(t, f, "GET", "/v1/me", "", nil)
	if st != 429 || hdr.Get("Retry-After") != "1" || p["code"] != "rate_limited" {
		t.Fatalf("Inject429 = %d %v %v", st, hdr, p)
	}
	f.Inject503(1)
	if st, _, p := call(t, f, "GET", "/v1/me", "", nil); st != 503 || p["code"] != "internal" {
		t.Fatalf("Inject503 = %d %v", st, p)
	}

	f.LoseNextCreateResponse()
	h := map[string]string{"Idempotency-Key": "lost"}
	if st, _, _ := call(t, f, "POST", "/v1/servers", `{"name":"b"}`, h); st != 503 {
		t.Fatalf("lost response = %d, want 503", st)
	}
	if len(f.Servers()) != 1 {
		t.Fatalf("lost response must still create the server, have %d", len(f.Servers()))
	}
	st, _, r := call(t, f, "POST", "/v1/servers", `{"name":"b"}`, h)
	if st != 200 || r["recovery"] == nil {
		t.Fatalf("retry after lost response = %d %v", st, r)
	}
}

func TestRecordingsNeverHoldAuthorization(t *testing.T) {
	f := newFake(t, fake.Options{})
	call(t, f, "POST", "/v1/servers?x=1", `{"name":"a"}`, map[string]string{"Idempotency-Key": "k"})
	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("recorded %d", len(reqs))
	}
	r := reqs[0]
	if r.Method != "POST" || r.Path != "/v1/servers" || r.Query != "x=1" || r.IdempotencyKey != "k" || r.Body != `{"name":"a"}` {
		t.Fatalf("recording = %+v", r)
	}
	if b, _ := json.Marshal(reqs); strings.Contains(string(b), "mon_test_fake") {
		t.Fatalf("recording holds the bearer: %s", b)
	}
}

func TestDashboardHelpers(t *testing.T) {
	f := newFake(t, fake.Options{})
	id := f.AddServer("a")
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	f.SetMuted(id, &until, "why")
	s := f.Servers()[0]
	if !s.AlertsMuted || s.AlertsMutedUntil == nil || !s.AlertsMutedUntil.Equal(until) || s.AlertsMutedReason != "why" {
		t.Fatalf("muted = %+v", s)
	}
	f.SetMuted(id, nil, "")
	if f.Servers()[0].AlertsMuted {
		t.Fatal("SetMuted(nil, \"\") must unmute")
	}
	past := time.Now().Add(-time.Minute)
	f.SetMuted(id, &past, "old")
	if f.Servers()[0].AlertsMuted {
		t.Fatal("a mute whose until has passed must read as unmuted")
	}
	f.SetTemplate(id, 2)
	if f.Servers()[0].AlertTemplateID != 2 {
		t.Fatal("SetTemplate")
	}
	f.DeleteServer(id)
	if len(f.Servers()) != 0 {
		t.Fatal("DeleteServer")
	}
}

func TestNewServerIsOfflineNotDormant(t *testing.T) {
	f := newFake(t, fake.Options{})
	f.AddServer("a")
	if s := f.Servers()[0]; s.Status != "offline" || s.Triage != "unreachable" {
		t.Fatalf("status/triage = %q/%q, want offline/unreachable", s.Status, s.Triage)
	}
}

func TestReplayOfDeletedServerIs404(t *testing.T) {
	f := newFake(t, fake.Options{})
	h := map[string]string{"Idempotency-Key": "gone"}
	call(t, f, "POST", "/v1/servers", `{"name":"a"}`, h)
	f.DeleteServer(f.Servers()[0].ID)
	st, _, p := call(t, f, "POST", "/v1/servers", `{"name":"a"}`, h)
	if st != 404 || p["code"] != "not_found" {
		t.Fatalf("replay after delete = %d %v, want 404 not_found", st, p)
	}
	if len(f.Servers()) != 0 {
		t.Fatal("a replay must never create a second server")
	}
}

func TestInjectIdempotencyInProgress(t *testing.T) {
	f := newFake(t, fake.Options{})
	f.InjectIdempotencyInProgress(1)
	h := map[string]string{"Idempotency-Key": "busy"}
	st, hdr, p := call(t, f, "POST", "/v1/servers", `{"name":"a"}`, h)
	if st != 503 || p["code"] != "idempotency_unavailable" || hdr.Get("Retry-After") != "2" ||
		p["detail"] != "a request with this Idempotency-Key is still in progress; retry shortly" ||
		p["title"] != "Idempotency store unavailable" {
		t.Fatalf("in-progress = %d %v %v", st, hdr, p)
	}
	if len(f.Servers()) != 0 {
		t.Fatal("the in-progress answer must not create anything")
	}
	if st, _, _ := call(t, f, "POST", "/v1/servers", `{"name":"a"}`, h); st != 201 {
		t.Fatalf("after the injection = %d, want 201", st)
	}
}

// The backend strips NUL and trims whitespace before it validates and stores a
// name (validation.SanitizeString); the fake does the same on create and rename.
func TestNameIsSanitized(t *testing.T) {
	f := newFake(t, fake.Options{})
	st, _, out := call(t, f, "POST", "/v1/servers", `{"name":"  web\u0000-1 "}`, map[string]string{"Idempotency-Key": "k1"})
	if st != 201 {
		t.Fatalf("create = %d, want 201", st)
	}
	if got := out["server"].(map[string]any)["name"]; got != "web-1" {
		t.Errorf("created name = %q, want %q", got, "web-1")
	}
	id := itoa(out["server"].(map[string]any)["id"].(float64))
	st, _, out = call(t, f, "PATCH", "/v1/servers/"+id, `{"name":"\tdb-1\u0000\n"}`, nil)
	if st != 200 || out["name"] != "db-1" {
		t.Errorf("rename = %d %q, want 200 %q", st, out["name"], "db-1")
	}
	if st, _, _ := call(t, f, "POST", "/v1/servers", `{"name":" \u0000 "}`, map[string]string{"Idempotency-Key": "k2"}); st != 400 {
		t.Errorf("create of a name that sanitizes to empty = %d, want 400", st)
	}
	if st, _, _ := call(t, f, "POST", "/v1/servers", `{"name":"`+strings.Repeat("é", 100)+`"}`, map[string]string{"Idempotency-Key": "k3"}); st != 201 {
		t.Errorf("create of 100 two-byte characters = %d, want 201", st)
	}
}

// The backend trims the reason and Postgres rounds until to a microsecond; the
// fake mirrors both so provider tests see what the real API returns.
func TestMuteTrimsReasonAndRoundsUntil(t *testing.T) {
	f := newFake(t, fake.Options{})
	id := f.AddServer("a")
	base := "/v1/servers/" + itoa(float64(id))

	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second).Add(123456789 * time.Nanosecond)
	body := `{"until":"` + until.Format(time.RFC3339Nano) + `","reason":"  deploy \t"}`
	st, _, m := call(t, f, "POST", base+"/alerts:mute", body, nil)
	if st != 200 || m["reason"] != "deploy" {
		t.Fatalf("mute = %d %v, want the reason trimmed", st, m)
	}
	want := until.Round(time.Microsecond).Format(time.RFC3339Nano)
	if m["until"] != want {
		t.Fatalf("until = %v, want %s (rounded to a microsecond)", m["until"], want)
	}
	// A reason that is only whitespace trims to empty.
	if st, _, m := call(t, f, "POST", base+"/alerts:mute", `{"reason":"   "}`, nil); st != 200 || m["reason"] != "" {
		t.Fatalf("blank reason = %d %v", st, m)
	}
	// A control character inside the text is still refused.
	if st, _, _ := call(t, f, "POST", base+"/alerts:mute", `{"reason":"a\nb"}`, nil); st != 400 {
		t.Fatalf("control character = %d", st)
	}
}
