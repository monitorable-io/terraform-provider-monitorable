package client

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

// operation is one REST call the client makes, with the Go types that carry
// its bodies. The table is the provider's whole API surface.
type operation struct {
	method, path string
	status       int
	req, resp    reflect.Type
	query        []string
}

func typeOf[T any]() reflect.Type { return reflect.TypeFor[T]() }

var operations = []operation{
	{method: "GET", path: "/v1/me", status: 200, resp: typeOf[Me]()},
	{method: "POST", path: "/v1/servers", status: 201, req: typeOf[createServerRequest](), resp: typeOf[CreateServerResponse]()},
	{method: "GET", path: "/v1/servers", status: 200, resp: typeOf[listServersResponse](), query: []string{"status", "q", "limit", "cursor"}},
	{method: "GET", path: "/v1/servers/{id}", status: 200, resp: typeOf[Server]()},
	{method: "PATCH", path: "/v1/servers/{id}", status: 200, req: typeOf[updateServerRequest](), resp: typeOf[Server]()},
	{method: "DELETE", path: "/v1/servers/{id}", status: 204},
	{method: "GET", path: "/v1/alert-templates", status: 200, resp: typeOf[listTemplatesResponse]()},
	{method: "GET", path: "/v1/notification-channels", status: 200, resp: typeOf[listChannelsResponse]()},
	{method: "PUT", path: "/v1/servers/{id}/alert-template", status: 200, req: typeOf[setTemplateRequest]()},
	{method: "POST", path: "/v1/servers/{id}/alerts:mute", status: 200, req: typeOf[muteRequest](), resp: typeOf[MuteState]()},
	{method: "POST", path: "/v1/servers/{id}/alerts:unmute", status: 200},
}

// The brief counts 12 operations: these 11 (method, path) pairs plus the 200
// replay of POST /v1/servers, which is asserted in the "replay" subtest.
const wantOperations = 11

type doc struct {
	Paths      map[string]map[string]map[string]any `json:"paths"`
	Components struct {
		Schemas map[string]map[string]any `json:"schemas"`
	} `json:"components"`
}

func loadSpec(t *testing.T) *doc {
	t.Helper()
	b, err := os.ReadFile("../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var d doc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

// resolve follows $ref and a single-element allOf.
func (d *doc) resolve(s map[string]any) map[string]any {
	for {
		if ref, ok := s["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/components/schemas/")
			next, ok := d.Components.Schemas[name]
			if !ok {
				return nil
			}
			s = next
			continue
		}
		if all, ok := s["allOf"].([]any); ok && len(all) == 1 {
			if m, ok := all[0].(map[string]any); ok {
				s = m
				continue
			}
		}
		return s
	}
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func schemaTypes(s map[string]any) []string {
	switch v := s["type"].(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			out = append(out, x.(string))
		}
		return out
	}
	return nil
}

func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

// check asserts that every JSON field of Go type t exists in schema s with a
// compatible wire type, recursing into nested structs and slices of structs.
func (d *doc) check(t *testing.T, where string, gt reflect.Type, s map[string]any) {
	t.Helper()
	s = d.resolve(s)
	if s == nil {
		t.Errorf("%s: schema does not resolve", where)
		return
	}
	for gt.Kind() == reflect.Ptr {
		gt = gt.Elem()
	}
	switch {
	case gt == reflect.TypeFor[time.Time]():
		if s["format"] != "date-time" {
			t.Errorf("%s: wire format %v, want date-time", where, s["format"])
		}
	case gt.Kind() == reflect.Struct:
		props := asMap(s["properties"])
		for i := 0; i < gt.NumField(); i++ {
			f := gt.Field(i)
			name := jsonName(f)
			if name == "" {
				t.Errorf("%s: Go field %s has no json tag", where, f.Name)
				continue
			}
			p, ok := props[name]
			if !ok {
				t.Errorf("%s: client reads %q, which is not in the schema", where, name)
				continue
			}
			d.check(t, where+"."+name, f.Type, asMap(p))
		}
	case gt.Kind() == reflect.Slice:
		if gt.Elem().Kind() == reflect.String && gt.Elem() == reflect.TypeFor[string]() {
			if !contains(schemaTypes(s), "array") {
				t.Errorf("%s: wire type %v, want array", where, schemaTypes(s))
			}
			return
		}
		d.check(t, where+"[]", gt.Elem(), asMap(s["items"]))
	case gt.Kind() == reflect.Int64:
		if !contains(schemaTypes(s), "integer") {
			t.Errorf("%s: wire type %v, want integer", where, schemaTypes(s))
		}
	case gt.Kind() == reflect.Float64:
		if !contains(schemaTypes(s), "number") {
			t.Errorf("%s: wire type %v, want number", where, schemaTypes(s))
		}
	case gt.Kind() == reflect.Bool:
		if !contains(schemaTypes(s), "boolean") {
			t.Errorf("%s: wire type %v, want boolean", where, schemaTypes(s))
		}
	case gt.Kind() == reflect.String:
		if !contains(schemaTypes(s), "string") {
			t.Errorf("%s: wire type %v, want string", where, schemaTypes(s))
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func jsonSchemaOf(op map[string]any, status string, request bool) map[string]any {
	var content map[string]any
	if request {
		content = asMap(asMap(op["requestBody"])["content"])
	} else {
		content = asMap(asMap(asMap(op["responses"])[status])["content"])
	}
	return asMap(asMap(content["application/json"])["schema"])
}

func TestEveryClientCallExistsInOpenAPI(t *testing.T) {
	d := loadSpec(t)
	if len(operations) != wantOperations {
		t.Fatalf("table has %d operations, want %d", len(operations), wantOperations)
	}
	for _, o := range operations {
		o := o
		t.Run(o.method+" "+o.path, func(t *testing.T) {
			path, ok := d.Paths[o.path]
			if !ok {
				t.Fatalf("path %s is not in openapi.json", o.path)
			}
			op, ok := path[strings.ToLower(o.method)]
			if !ok {
				t.Fatalf("%s %s is not in openapi.json", o.method, o.path)
			}
			statusKey := strconv.Itoa(o.status)
			if _, ok := asMap(op["responses"])[statusKey]; !ok {
				t.Fatalf("%s %s declares no %s response", o.method, o.path, statusKey)
			}
			if o.req != nil {
				s := jsonSchemaOf(op, "", true)
				if s == nil {
					t.Fatal("no JSON request body in the spec")
				}
				d.check(t, "request", o.req, s)
			}
			if o.resp != nil {
				s := jsonSchemaOf(op, statusKey, false)
				if s == nil {
					t.Fatalf("no JSON %s response body in the spec", statusKey)
				}
				d.check(t, "response", o.resp, s)
			}
			declared := map[string]bool{}
			params, _ := op["parameters"].([]any)
			for _, p := range params {
				pm := asMap(p)
				if pm["in"] == "query" {
					declared[pm["name"].(string)] = true
				}
			}
			for _, q := range o.query {
				if !declared[q] {
					t.Errorf("query parameter %q is not declared", q)
				}
			}
		})
	}

	t.Run("replay", func(t *testing.T) {
		op := d.Paths["/v1/servers"]["post"]
		s := jsonSchemaOf(op, "200", false)
		if s == nil {
			t.Fatal("POST /v1/servers declares no JSON 200 replay response")
		}
		d.check(t, "replay", reflect.TypeFor[CreateServerResponse](), s)
	})
}

var idSegment = regexp.MustCompile(`/v1/servers/\d+`)

// TestClientIssuesExactlyTheTabledCalls drives every client method against
// the fake and compares what went over the wire to the table, so the table
// cannot drift from the code.
func TestClientIssuesExactlyTheTabledCalls(t *testing.T) {
	f := newFake(t, fake.Options{})
	c := newTestClient(t, f)
	ctx := context.Background()
	id := f.AddServer("a")
	until := time.Now().Add(time.Hour)

	_, _ = c.Me(ctx)
	_, _ = c.CreateServer(ctx, "b", "k")
	_, _ = c.GetServer(ctx, id)
	_, _ = c.UpdateServer(ctx, id, "c")
	_, _ = c.ListServers(ctx, ListServersParams{})
	_, _ = c.ListAlertTemplates(ctx)
	_, _ = c.ListNotificationChannels(ctx)
	_ = c.SetServerAlertTemplate(ctx, id, 2)
	_, _ = c.MuteServerAlerts(ctx, id, &until, "r")
	_ = c.UnmuteServerAlerts(ctx, id)
	_ = c.DeleteServer(ctx, id)

	got := map[string]bool{}
	for _, r := range f.Requests() {
		got[r.Method+" "+idSegment.ReplaceAllString(r.Path, "/v1/servers/{id}")] = true
	}
	want := map[string]bool{}
	for _, o := range operations {
		want[o.method+" "+o.path] = true
	}
	var extra, missing []string
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("client calls not in the table: %v; table rows never called: %v", extra, missing)
	}
}
