// Package fake is an in-process stand-in for the Monitorable REST v1 API. It
// implements exactly the operations the provider's client uses, with the
// statuses and problem codes of the real API, so the client tests and the
// provider's acceptance suite can run without a network.
//
// It accepts any bearer starting with "mon_test_fake"; every other credential
// is a 401. It never records the Authorization header.
package fake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const keyPrefix = "mon_test_fake"

// Server mirrors the wire Server object.
type Server struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	Status            string     `json:"status"`
	Triage            string     `json:"triage"`
	LastSeenAt        *time.Time `json:"last_seen_at"`
	CreatedAt         *time.Time `json:"created_at"`
	IP                string     `json:"ip"`
	IPv4              string     `json:"ipv4"`
	IPv6              string     `json:"ipv6"`
	Location          string     `json:"location"`
	CountryCode       string     `json:"country_code"`
	AgentVersion      string     `json:"agent_version"`
	UptimeSeconds     float64    `json:"uptime_seconds"`
	AlertsMuted       bool       `json:"alerts_muted"`
	AlertsMutedUntil  *time.Time `json:"alerts_muted_until"`
	AlertsMutedReason string     `json:"alerts_muted_reason"`
	AlertTemplateID   int64      `json:"alert_template_id"`
	OpenIncidents     int64      `json:"open_incidents"`
	UnackedFiring     int64      `json:"unacked_firing"`
	CreatedVia        string     `json:"created_via"`
}

// Options configures a Fake.
type Options struct {
	MaxServers int      // 0 = unlimited
	Scopes     []string // default: servers:read servers:write alerts:read alerts:write
	Role       string   // default admin
}

// RecordedRequest is what the fake saw of one request. It never holds the
// Authorization header.
type RecordedRequest struct {
	Method         string
	Path           string // without the query string
	Query          string // raw query string
	IdempotencyKey string
	Body           string
}

type idemRecord struct {
	name     string
	serverID int64
}

type injection struct {
	method, path string // empty = any request
	status       int
	n            int
}

// Fake is the in-process API.
type Fake struct {
	srv *httptest.Server
	opt Options

	mu         sync.Mutex
	servers    map[int64]*Server
	nextID     int64
	keySeq     int
	reqSeq     int
	idem       map[string]idemRecord
	requests   []RecordedRequest
	injections []*injection
	loseCreate bool
}

// New starts a fake. Callers use t.Cleanup(f.Close).
func New(opts Options) *Fake {
	if opts.Scopes == nil {
		opts.Scopes = []string{"servers:read", "servers:write", "alerts:read", "alerts:write"}
	}
	if opts.Role == "" {
		opts.Role = "admin"
	}
	f := &Fake{opt: opts, servers: map[int64]*Server{}, nextID: 1, idem: map[string]idemRecord{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// Close stops the server.
func (f *Fake) Close() { f.srv.Close() }

// URL is the base URL of the fake.
func (f *Fake) URL() string { return f.srv.URL }

// Servers returns a snapshot ordered by id.
func (f *Fake) Servers() []Server {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sortedLocked(time.Now())
}

// AddServer seeds a server without a request and returns its id.
func (f *Fake) AddServer(name string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addLocked(name, "dashboard").ID
}

// DeleteServer simulates a deletion in the dashboard.
func (f *Fake) DeleteServer(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.servers, id)
}

// SetMuted simulates the dashboard (or the clock): (nil, "") unmutes; an
// until in the past reads back as unmuted, like the real API.
func (f *Fake) SetMuted(id int64, until *time.Time, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.servers[id]
	if !ok {
		return
	}
	if until == nil && reason == "" {
		s.AlertsMuted, s.AlertsMutedUntil, s.AlertsMutedReason = false, nil, ""
		return
	}
	s.AlertsMuted, s.AlertsMutedReason = true, reason
	s.AlertsMutedUntil = nil
	if until != nil {
		u := until.UTC()
		s.AlertsMutedUntil = &u
	}
}

// SetTemplate simulates assigning a template in the dashboard.
func (f *Fake) SetTemplate(id, templateID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.servers[id]; ok {
		s.AlertTemplateID = templateID
	}
}

// Inject429 makes the next n requests answer 429 with Retry-After: 1.
func (f *Fake) Inject429(n int) { f.inject("", "", 429, n) }

// Inject503 makes the next n requests answer 503 internal.
func (f *Fake) Inject503(n int) { f.inject("", "", 503, n) }

// InjectStatusOn makes the next n requests with exactly this method and path
// (no query string) answer status with a problem body.
func (f *Fake) InjectStatusOn(method, path string, status, n int) { f.inject(method, path, status, n) }

func (f *Fake) inject(method, path string, status, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.injections = append(f.injections, &injection{method: method, path: path, status: status, n: n})
}

// LoseNextCreateResponse makes the next POST /v1/servers be fully processed
// (server created, idempotency recorded) and then answered 503 internal, as if
// the response was lost on the way back.
func (f *Fake) LoseNextCreateResponse() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loseCreate = true
}

// Requests returns every request received, in order.
func (f *Fake) Requests() []RecordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// ---- state helpers (callers hold f.mu) ----

func (f *Fake) addLocked(name, via string) *Server {
	now := time.Now().UTC().Truncate(time.Second)
	s := &Server{ID: f.nextID, Name: name, Status: "dormant", Triage: "dormant", CreatedAt: &now, CreatedVia: via}
	f.nextID++
	f.servers[s.ID] = s
	return s
}

// view returns the server as the API shows it at now: a mute whose until has
// passed reads as unmuted.
func view(s *Server, now time.Time) Server {
	v := *s
	if v.AlertsMuted && v.AlertsMutedUntil != nil && !v.AlertsMutedUntil.After(now) {
		v.AlertsMuted, v.AlertsMutedUntil, v.AlertsMutedReason = false, nil, ""
	}
	if !v.AlertsMuted {
		v.AlertsMutedUntil, v.AlertsMutedReason = nil, ""
	}
	return v
}

func (f *Fake) sortedLocked(now time.Time) []Server {
	out := make([]Server, 0, len(f.servers))
	for _, s := range f.servers {
		out = append(out, view(s, now))
	}
	slices.SortFunc(out, func(a, b Server) int { return int(a.ID - b.ID) })
	return out
}

// ---- HTTP ----

type problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Code      string `json:"code"`
	RequestID string `json:"request_id"`
	Class     string `json:"class,omitempty"`
}

type reply struct {
	status int
	body   any
	hdr    map[string]string
}

func fail(status int, code, detail string) reply {
	return reply{status: status, body: problem{
		Type:   "https://api.monitorable.io/problems/" + code,
		Title:  http.StatusText(status),
		Status: status, Detail: detail, Code: code,
	}}
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	rec := RecordedRequest{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		IdempotencyKey: r.Header.Get("Idempotency-Key"), Body: string(raw),
	}
	authed := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+keyPrefix)

	f.mu.Lock()
	f.requests = append(f.requests, rec)
	f.reqSeq++
	reqID := fmt.Sprintf("req_fake_%d", f.reqSeq)
	rep := f.handleLocked(r, rec, raw, authed)
	f.mu.Unlock()

	if p, ok := rep.body.(problem); ok {
		p.RequestID = reqID
		rep.body = p
	}
	h := w.Header()
	h.Set("X-Request-Id", reqID)
	h.Set("RateLimit-Limit", "60")
	h.Set("RateLimit-Remaining", "59")
	h.Set("RateLimit-Reset", "60")
	for k, v := range rep.hdr {
		h.Set(k, v)
	}
	if rep.status == http.StatusNoContent {
		w.WriteHeader(rep.status)
		return
	}
	if _, isProblem := rep.body.(problem); isProblem {
		h.Set("Content-Type", "application/problem+json")
	} else {
		h.Set("Content-Type", "application/json")
	}
	w.WriteHeader(rep.status)
	_ = json.NewEncoder(w).Encode(rep.body)
}

func (f *Fake) takeInjectionLocked(method, path string) *injection {
	for _, in := range f.injections {
		if in.n > 0 && in.method != "" && in.method == method && in.path == path {
			in.n--
			return in
		}
	}
	for _, in := range f.injections {
		if in.n > 0 && in.method == "" {
			in.n--
			return in
		}
	}
	return nil
}

func injected(status int) reply {
	switch status {
	case 429:
		rep := fail(429, "rate_limited", "rate limit exceeded")
		p := rep.body.(problem)
		p.Class = "mutate"
		rep.body = p
		rep.hdr = map[string]string{"Retry-After": "1"}
		return rep
	case 503:
		return fail(503, "internal", "service unavailable")
	default:
		return fail(status, "internal", "injected failure")
	}
}

func (f *Fake) handleLocked(r *http.Request, rec RecordedRequest, body []byte, authed bool) reply {
	if in := f.takeInjectionLocked(r.Method, r.URL.Path); in != nil {
		return injected(in.status)
	}
	if !authed {
		return fail(401, "invalid_api_key", "the API key is missing or invalid")
	}
	now := time.Now()
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segs) < 2 || segs[0] != "v1" {
		return fail(404, "not_found", "no such endpoint")
	}

	type route struct {
		scope string
		write bool
		run   func() reply
	}
	var rt route
	switch {
	case len(segs) == 2 && segs[1] == "me" && r.Method == "GET":
		rt = route{run: func() reply { return f.me() }}
	case len(segs) == 2 && segs[1] == "servers" && r.Method == "POST":
		rt = route{"servers:write", true, func() reply { return f.createLocked(rec, body, now) }}
	case len(segs) == 2 && segs[1] == "servers" && r.Method == "GET":
		rt = route{"servers:read", false, func() reply { return f.listLocked(r.URL.Query(), now) }}
	case len(segs) == 2 && segs[1] == "alert-templates" && r.Method == "GET":
		rt = route{"alerts:read", false, func() reply { return reply{status: 200, body: map[string]any{"data": templates()}} }}
	case len(segs) == 2 && segs[1] == "notification-channels" && r.Method == "GET":
		rt = route{"alerts:read", false, func() reply { return reply{status: 200, body: map[string]any{"data": channels()}} }}
	case len(segs) >= 3 && segs[1] == "servers":
		id, err := strconv.ParseInt(segs[2], 10, 64)
		if err != nil || id <= 0 {
			return fail(400, "invalid_argument", "id must be a positive integer")
		}
		switch {
		case len(segs) == 3 && r.Method == "GET":
			rt = route{"servers:read", false, func() reply { return f.getLocked(id, now) }}
		case len(segs) == 3 && r.Method == "PATCH":
			rt = route{"servers:write", true, func() reply { return f.patchLocked(id, body, now) }}
		case len(segs) == 3 && r.Method == "DELETE":
			rt = route{"servers:write", true, func() reply { return f.deleteLocked(id) }}
		case len(segs) == 4 && segs[3] == "alert-template" && r.Method == "PUT":
			rt = route{"alerts:write", true, func() reply { return f.setTemplateLocked(id, body) }}
		case len(segs) == 4 && segs[3] == "alerts:mute" && r.Method == "POST":
			rt = route{"alerts:write", true, func() reply { return f.muteLocked(id, body, now) }}
		case len(segs) == 4 && segs[3] == "alerts:unmute" && r.Method == "POST":
			rt = route{"alerts:write", true, func() reply { return f.unmuteLocked(id) }}
		}
	}
	if rt.run == nil {
		return fail(404, "not_found", "no such endpoint")
	}
	if rt.scope != "" && !slices.Contains(f.opt.Scopes, rt.scope) {
		return fail(403, "insufficient_scope", "requires scope "+rt.scope)
	}
	if rt.write && f.opt.Role != "admin" {
		return fail(403, "insufficient_role", "requires the admin role")
	}

	isCreate := r.Method == "POST" && r.URL.Path == "/v1/servers"
	lose := isCreate && f.loseCreate
	if isCreate {
		f.loseCreate = false
	}
	rep := rt.run()
	if lose && rep.status/100 == 2 {
		return fail(503, "internal", "service unavailable")
	}
	return rep
}

func decode(body []byte, v any) *reply {
	if len(body) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		r := fail(400, "invalid_argument", "invalid request body: "+err.Error())
		return &r
	}
	return nil
}

func validName(name string) *reply {
	if strings.TrimSpace(name) == "" {
		r := fail(400, "invalid_argument", "name must not be empty")
		return &r
	}
	if len([]rune(name)) > 100 {
		r := fail(400, "invalid_argument", "name must be at most 100 characters")
		return &r
	}
	return nil
}

func (f *Fake) me() reply {
	return reply{status: 200, body: map[string]any{
		"key_id": 1, "key_name": "fake", "scopes": f.opt.Scopes, "role": f.opt.Role,
		"organization_id": 1, "organization_name": "Fake Org", "plan_name": "Pro", "expires_at": nil,
	}}
}

func templates() []map[string]any {
	return []map[string]any{
		{"id": 1, "name": "Default", "is_default": true, "reminder_interval_minutes": 0, "notify_on_resolve": true,
			"escalate_after_minutes": 0, "rules": []any{}, "channel_ids": []int{1}, "escalation_channel_ids": []int{}},
		{"id": 2, "name": "Strict", "is_default": false, "reminder_interval_minutes": 30, "notify_on_resolve": true,
			"escalate_after_minutes": 60, "rules": []any{}, "channel_ids": []int{1, 2}, "escalation_channel_ids": []int{2}},
	}
}

func channels() []map[string]any {
	return []map[string]any{
		{"id": 1, "type": "email", "name": "Ops", "enabled": true, "last_delivery_at": nil, "last_delivery_ok": false},
		{"id": 2, "type": "slack", "name": "Alerts", "enabled": true, "last_delivery_at": nil, "last_delivery_ok": false},
	}
}

func (f *Fake) createLocked(rec RecordedRequest, body []byte, now time.Time) reply {
	var in struct {
		Name string `json:"name"`
	}
	if bad := decode(body, &in); bad != nil {
		return *bad
	}
	if bad := validName(in.Name); bad != nil {
		return *bad
	}
	if key := rec.IdempotencyKey; key != "" {
		if prev, ok := f.idem[key]; ok {
			if prev.name != in.Name {
				return fail(422, "idempotency_key_reused", "the Idempotency-Key was used with a different request body")
			}
			var sv Server
			if s, ok := f.servers[prev.serverID]; ok {
				sv = view(s, now)
			} else {
				sv = Server{ID: prev.serverID, Name: prev.name}
			}
			return reply{status: 200, body: map[string]any{
				"server":   sv,
				"recovery": fmt.Sprintf("POST /v1/servers/%d/agent-key:rotate", prev.serverID),
			}}
		}
	}
	if f.opt.MaxServers > 0 && len(f.servers) >= f.opt.MaxServers {
		return fail(402, "plan_limit_reached", fmt.Sprintf("max_servers is %d on your plan", f.opt.MaxServers))
	}
	s := f.addLocked(in.Name, "api")
	if rec.IdempotencyKey != "" {
		f.idem[rec.IdempotencyKey] = idemRecord{name: in.Name, serverID: s.ID}
	}
	f.keySeq++
	key := fmt.Sprintf("mon_agent_fake_%d_%d", s.ID, f.keySeq)
	return reply{status: 201, body: map[string]any{
		"server":          view(s, now),
		"agent_api_key":   key,
		"install_command": "curl -fsSL https://get.monitorable.net/install.sh | sudo sh -s -- --api-key=" + key,
	}}
}

func (f *Fake) getLocked(id int64, now time.Time) reply {
	s, ok := f.servers[id]
	if !ok {
		return fail(404, "not_found", "server not found")
	}
	return reply{status: 200, body: view(s, now)}
}

func (f *Fake) patchLocked(id int64, body []byte, now time.Time) reply {
	var in struct {
		Name string `json:"name"`
	}
	if bad := decode(body, &in); bad != nil {
		return *bad
	}
	s, ok := f.servers[id]
	if !ok {
		return fail(404, "not_found", "server not found")
	}
	if bad := validName(in.Name); bad != nil {
		return *bad
	}
	s.Name = in.Name
	return reply{status: 200, body: view(s, now)}
}

func (f *Fake) deleteLocked(id int64) reply {
	if _, ok := f.servers[id]; !ok {
		return fail(404, "not_found", "server not found")
	}
	delete(f.servers, id)
	return reply{status: 204}
}

func (f *Fake) listLocked(q url.Values, now time.Time) reply {
	status := q.Get("status")
	switch status {
	case "", "online", "stale", "offline", "dormant":
	default:
		return fail(400, "invalid_argument", "status must be online, stale, offline or dormant")
	}
	limit := 25
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			return fail(400, "invalid_argument", "limit must be between 1 and 100")
		}
		limit = min(n, 100)
	}
	var after int64
	if c := q.Get("cursor"); c != "" {
		n, err := strconv.ParseInt(strings.TrimPrefix(c, "fake_"), 10, 64)
		if err != nil || !strings.HasPrefix(c, "fake_") {
			return fail(400, "invalid_argument", "cursor is not valid")
		}
		after = n
	}
	needle := strings.ToLower(q.Get("q"))
	var match []Server
	for _, s := range f.sortedLocked(now) {
		if s.ID <= after || (status != "" && s.Status != status) ||
			(needle != "" && !strings.Contains(strings.ToLower(s.Name), needle)) {
			continue
		}
		match = append(match, s)
	}
	next := ""
	if len(match) > limit {
		match = match[:limit]
		next = "fake_" + strconv.FormatInt(match[len(match)-1].ID, 10)
	}
	if match == nil {
		match = []Server{}
	}
	return reply{status: 200, body: map[string]any{"data": match, "next_cursor": next}}
}

func (f *Fake) setTemplateLocked(id int64, body []byte) reply {
	var in struct {
		TemplateID *int64 `json:"template_id"`
	}
	if bad := decode(body, &in); bad != nil {
		return *bad
	}
	s, ok := f.servers[id]
	if !ok {
		return fail(404, "not_found", "server not found")
	}
	var tid int64
	if in.TemplateID != nil {
		tid = *in.TemplateID
	}
	if tid < 0 {
		return fail(400, "invalid_argument", "template_id must not be negative")
	}
	if tid != 0 && tid != 1 && tid != 2 {
		return fail(404, "not_found", "alert template not found")
	}
	s.AlertTemplateID = tid
	return reply{status: 200, body: map[string]any{"server_id": id, "template_id": tid}}
}

func (f *Fake) muteLocked(id int64, body []byte, now time.Time) reply {
	var in struct {
		Until  *string `json:"until"`
		Reason string  `json:"reason"`
	}
	if bad := decode(body, &in); bad != nil {
		return *bad
	}
	s, ok := f.servers[id]
	if !ok {
		return fail(404, "not_found", "server not found")
	}
	var until *time.Time
	if in.Until != nil {
		u, err := time.Parse(time.RFC3339, *in.Until)
		if err != nil {
			return fail(400, "invalid_argument", "until must be an RFC 3339 timestamp")
		}
		u = u.UTC()
		if !u.After(now) {
			return fail(400, "invalid_argument", "until must be in the future")
		}
		if u.After(now.Add(366 * 24 * time.Hour)) {
			return fail(400, "invalid_argument", "until must be at most 366 days ahead")
		}
		until = &u
	}
	if len([]rune(in.Reason)) > 200 {
		return fail(400, "invalid_argument", "reason must be at most 200 characters")
	}
	if strings.IndexFunc(in.Reason, unicode.IsControl) >= 0 {
		return fail(400, "invalid_argument", "reason must not contain control characters")
	}
	s.AlertsMuted, s.AlertsMutedUntil, s.AlertsMutedReason = true, until, in.Reason
	return reply{status: 200, body: muteState(id, true, until, in.Reason)}
}

func (f *Fake) unmuteLocked(id int64) reply {
	s, ok := f.servers[id]
	if !ok {
		return fail(404, "not_found", "server not found")
	}
	s.AlertsMuted, s.AlertsMutedUntil, s.AlertsMutedReason = false, nil, ""
	return reply{status: 200, body: muteState(id, false, nil, "")}
}

func muteState(id int64, muted bool, until *time.Time, reason string) map[string]any {
	return map[string]any{"server_id": id, "muted": muted, "until": until, "reason": reason}
}
