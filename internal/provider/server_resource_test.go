package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

const serverAddr = "monitorable_server.test"

var (
	uuidV4  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	decimal = regexp.MustCompile(`^[1-9][0-9]*$`)
)

func serverConfig(name string) string {
	return fmt.Sprintf("resource \"monitorable_server\" \"test\" {\n  name = %q\n}\n", name)
}

// testFakeClient is a client for a dedicated fake from newTestFake.
func testFakeClient(t *testing.T, f *fake.Fake) *client.Client {
	t.Helper()
	c, err := client.New(f.URL(), accKey+"-"+t.Name(), "")
	if err != nil {
		t.Fatalf("building the fake client: %v", err)
	}
	return c
}

// testAccCheckServerDestroyed is a CheckDestroy: every monitorable_server left
// in the state must be gone from the API (GetServer answers not found).
func testAccCheckServerDestroyed(c *client.Client) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for _, rs := range s.RootModule().Resources {
			if rs.Type != "monitorable_server" {
				continue
			}
			var diags diag.Diagnostics
			id, ok := parseID(path.Root("id"), rs.Primary.ID, &diags)
			if !ok {
				return fmt.Errorf("monitorable_server id %q in state is not a decimal integer", rs.Primary.ID)
			}
			_, err := c.GetServer(context.Background(), id)
			switch {
			case err == nil:
				return fmt.Errorf("server %d still exists after destroy", id)
			case !client.IsNotFound(err):
				return fmt.Errorf("checking server %d after destroy: %w", id, err)
			}
		}
		return nil
	}
}

// captureID stores the resource's id in *dst.
func captureID(addr string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		*dst = rs.Primary.ID
		return nil
	}
}

// createPosts returns the POST /v1/servers requests the fake saw.
func createPosts(f *fake.Fake) []fake.RecordedRequest {
	var out []fake.RecordedRequest
	for _, r := range f.Requests() {
		if r.Method == "POST" && r.Path == "/v1/servers" {
			out = append(out, r)
		}
	}
	return out
}

func TestServerCreateUpdateDestroy(t *testing.T) {
	testAccPreCheck(t)
	nameA, nameB := testAccName("cud-a"), testAccName("cud-b")
	var firstID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{
				Config: serverConfig(nameA),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(serverAddr, "id", decimal),
					resource.TestCheckResourceAttr(serverAddr, "name", nameA),
					resource.TestCheckResourceAttrSet(serverAddr, "agent_api_key"),
					resource.TestCheckResourceAttrSet(serverAddr, "install_command"),
					resource.TestCheckResourceAttrSet(serverAddr, "created_at"),
					resource.TestCheckResourceAttr(serverAddr, "created_via", "api"),
					func(s *terraform.State) error {
						a := s.RootModule().Resources[serverAddr].Primary.Attributes
						if !strings.Contains(a["install_command"], a["agent_api_key"]) {
							return fmt.Errorf("install_command does not contain agent_api_key")
						}
						return nil
					},
					captureID(serverAddr, &firstID),
				),
			},
			{
				Config: serverConfig(nameB),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(serverAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(serverAddr, "name", nameB),
					resource.TestCheckResourceAttrSet(serverAddr, "agent_api_key"),
					func(s *terraform.State) error {
						if id := s.RootModule().Resources[serverAddr].Primary.ID; id != firstID {
							return fmt.Errorf("id changed on rename: %s -> %s", firstID, id)
						}
						if accFake == nil {
							return nil
						}
						for _, r := range accFake.Requests() {
							if r.Method == "PATCH" && r.Path == "/v1/servers/"+firstID && strings.Contains(r.Body, nameB) {
								return nil
							}
						}
						return fmt.Errorf("the fake saw no PATCH /v1/servers/%s renaming to %s", firstID, nameB)
					},
				),
			},
		},
	})
}

func TestServerCreateSendsIdempotencyKey(t *testing.T) {
	f, pb := newTestFake(t, fake.Options{})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testFakeClient(t, f)),
		Steps: []resource.TestStep{{
			Config: pb + serverConfig("web"),
			Check: func(*terraform.State) error {
				posts := createPosts(f)
				if len(posts) != 1 {
					return fmt.Errorf("POST /v1/servers sent %d times, want 1", len(posts))
				}
				if !uuidV4.MatchString(posts[0].IdempotencyKey) {
					return fmt.Errorf("Idempotency-Key %q is not a UUID", posts[0].IdempotencyKey)
				}
				return nil
			},
		}},
	})
}

// Review Focus 1: a transient failure of the create is retried with the same
// Idempotency-Key and never makes a second server.
func TestServerCreateRetriesSameIdempotencyKey(t *testing.T) {
	f, pb := newTestFake(t, fake.Options{})
	f.InjectStatusOn("POST", "/v1/servers", 503, 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testFakeClient(t, f)),
		Steps: []resource.TestStep{{
			Config: pb + serverConfig("web"),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrSet(serverAddr, "agent_api_key"),
				func(*terraform.State) error {
					posts := createPosts(f)
					if len(posts) != 2 {
						return fmt.Errorf("POST /v1/servers sent %d times, want 2", len(posts))
					}
					if posts[0].IdempotencyKey == "" || posts[0].IdempotencyKey != posts[1].IdempotencyKey {
						return fmt.Errorf("retry changed the Idempotency-Key: %q then %q", posts[0].IdempotencyKey, posts[1].IdempotencyKey)
					}
					if n := len(f.Servers()); n != 1 {
						return fmt.Errorf("%d servers exist, want 1", n)
					}
					return nil
				},
			),
		}},
	})
}

// Review Focus 1: the 201 is lost and the retry gets the replay, which has no
// key. The server is adopted (one server, no key in state) and the next plan
// is empty. The warning itself is asserted by TestServerCreateReplayWarning:
// terraform-plugin-testing has no way to assert a warning diagnostic.
func TestServerCreateReplayAdoptsWithWarning(t *testing.T) {
	f, pb := newTestFake(t, fake.Options{})
	f.LoseNextCreateResponse()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testFakeClient(t, f)),
		Steps: []resource.TestStep{
			{
				Config: pb + serverConfig("web"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(serverAddr, "id", decimal),
					resource.TestCheckResourceAttr(serverAddr, "name", "web"),
					resource.TestCheckNoResourceAttr(serverAddr, "agent_api_key"),
					resource.TestCheckNoResourceAttr(serverAddr, "install_command"),
					func(*terraform.State) error {
						if n := len(f.Servers()); n != 1 {
							return fmt.Errorf("%d servers exist, want 1", n)
						}
						posts := createPosts(f)
						if len(posts) != 2 || posts[0].IdempotencyKey != posts[1].IdempotencyKey {
							return fmt.Errorf("want 2 POSTs with one Idempotency-Key, got %d", len(posts))
						}
						return nil
					},
				),
			},
			{
				Config:   pb + serverConfig("web"),
				PlanOnly: true,
			},
		},
	})
}

// Review Focus 3: a server deleted in the dashboard is dropped from state, so
// the plan re-creates it instead of failing.
func TestServerReadGoneRemovesFromState(t *testing.T) {
	f, pb := newTestFake(t, fake.Options{})
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testFakeClient(t, f)),
		Steps: []resource.TestStep{
			{
				Config: pb + serverConfig("web"),
				Check:  captureID(serverAddr, &id),
			},
			{
				PreConfig: func() {
					var diags diag.Diagnostics
					n, ok := parseID(path.Root("id"), id, &diags)
					if !ok {
						t.Fatalf("id %q from state is not a decimal integer", id)
					}
					f.DeleteServer(n)
				},
				Config:             pb + serverConfig("web"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectResourceAction(serverAddr, plancheck.ResourceActionCreate)},
				},
			},
		},
	})
}

// Review Focus 1: an orphan (a server Terraform lost track of) is adopted by
// terraform import, without a key. It runs on a real endpoint too.
func TestServerImport(t *testing.T) {
	testAccPreCheck(t)
	name := testAccName("import")
	var id int64
	if accFake == nil {
		c := testAccClient(t)
		out, err := c.CreateServer(context.Background(), name, uuid.NewString())
		if err != nil {
			t.Fatalf("creating the server to import: %v", err)
		}
		id = out.Server.ID
		t.Cleanup(func() {
			if err := c.DeleteServer(context.Background(), id); err != nil && !client.IsNotFound(err) {
				t.Errorf("deleting server %d: %v", id, err)
			}
		})
	} else {
		id = accFake.AddServer(name)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{
				Config:             serverConfig(name),
				ResourceName:       serverAddr,
				ImportState:        true,
				ImportStateId:      idString(id),
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d resources, want 1", len(states))
					}
					a := states[0].Attributes
					if a["id"] != idString(id) || a["name"] != name {
						return fmt.Errorf("imported id %q name %q, want %q %q", a["id"], a["name"], idString(id), name)
					}
					for _, k := range []string{"agent_api_key", "install_command"} {
						if _, ok := a[k]; ok {
							return fmt.Errorf("%s is in state after import; want null", k)
						}
					}
					if a["created_at"] == "" {
						return fmt.Errorf("created_at is empty after import")
					}
					if accFake != nil && a["created_via"] != "dashboard" {
						return fmt.Errorf("created_via = %q, want the API's %q", a["created_via"], "dashboard")
					}
					return nil
				},
			},
			{
				// The imported server matches the configuration: nothing to do.
				Config:   serverConfig(name),
				PlanOnly: true,
			},
			{
				Config:                  serverConfig(name),
				ResourceName:            serverAddr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"agent_api_key", "install_command"},
			},
		},
	})
}

func TestServerImportInvalidID(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        serverConfig(testAccName("bad-import")),
			ResourceName:  serverAddr,
			ImportState:   true,
			ImportStateId: "web-1",
			ExpectError:   regexp.MustCompile(`id must be a decimal integer, got "web-1"`),
		}},
	})
}

// Importing an id the API does not know names it, as the child resources do.
func TestServerImportNotFound(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        serverConfig(testAccName("missing-import")),
			ResourceName:  serverAddr,
			ImportState:   true,
			ImportStateId: "2147483000",
			ExpectError:   regexp.MustCompile(`server 2147483000 not found`),
		}},
	})
}

// Review Focus 4: a read-only key cannot create; the diagnostic names the
// missing scope and nothing is created or written to state.
func TestServerCreateForbiddenDiagnostic(t *testing.T) {
	f, pb := newTestFake(t, fake.Options{Scopes: []string{"servers:read"}})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      pb + serverConfig("web"),
			ExpectError: regexp.MustCompile(`(?s)Creating the server failed.*insufficient_scope`),
		}},
	})
	if n := len(f.Servers()); n != 0 {
		t.Errorf("%d servers exist after a forbidden create, want 0", n)
	}
}

func TestServerCreatePlanLimitDiagnostic(t *testing.T) {
	f, pb := newTestFake(t, fake.Options{MaxServers: 1})
	cfg := pb + `
resource "monitorable_server" "a" {
  name = "web-a"
}

resource "monitorable_server" "b" {
  name = "web-b"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testFakeClient(t, f)),
		Steps: []resource.TestStep{{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`(?s)plan_limit_reached.*Raise\s+the\s+plan\s+limit\s+at\s+https://app\.monitorable\.io/billing`),
		}},
	})
}

func TestServerNameValidation(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      serverConfig(""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`character\s+count\s+must\s+be\s+between\s+1\s+and\s+100`),
			},
			{
				Config:      serverConfig("web "),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`name\s+must\s+not\s+start\s+or\s+end\s+with\s+whitespace\s+or\s+contain\s+NUL`),
			},
		},
	})
}

// serverSchema returns the resource's schema.
func serverSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &fwresource.SchemaResponse{}
	NewServerResource().Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func TestServerStateIsSensitive(t *testing.T) {
	s := serverSchema(t)
	for _, k := range []string{"agent_api_key", "install_command"} {
		a, ok := s.Attributes[k]
		if !ok {
			t.Fatalf("schema has no %s", k)
		}
		if !a.IsSensitive() || !a.IsComputed() || a.IsOptional() || a.IsRequired() {
			t.Errorf("%s: sensitive=%v computed=%v optional=%v required=%v; want sensitive, computed only",
				k, a.IsSensitive(), a.IsComputed(), a.IsOptional(), a.IsRequired())
		}
	}
	for k, a := range s.Attributes {
		if k != "agent_api_key" && k != "install_command" && a.IsSensitive() {
			t.Errorf("%s is sensitive; only the key and the install command are", k)
		}
	}
}

// The name validators count characters, not bytes (the backend's limit is 100
// characters), and refuse what the backend would silently rewrite.
func TestServerNameValidators(t *testing.T) {
	vs := serverSchema(t).Attributes["name"].(schema.StringAttribute).Validators
	cases := []struct {
		name string
		ok   bool
	}{
		{"web-01", true},
		{strings.Repeat("é", 100), true},
		{strings.Repeat("界", 100), true},
		{strings.Repeat("a", 100), true},
		{strings.Repeat("é", 101), false},
		{"", false},
		{"web ", false},
		{" web", false},
		{"\tweb", false},
		{"web\n", false},
		{"\u00a0web", false}, // NO-BREAK SPACE: strings.TrimSpace trims it too
		{"web\x00", false},
		{"we\x00b", false},
		{"web server", true},
	}
	for _, tc := range cases {
		var diags diag.Diagnostics
		for _, v := range vs {
			resp := &validator.StringResponse{}
			v.ValidateString(context.Background(), validator.StringRequest{
				Path: path.Root("name"), ConfigValue: types.StringValue(tc.name),
			}, resp)
			diags.Append(resp.Diagnostics...)
		}
		if got := !diags.HasError(); got != tc.ok {
			t.Errorf("name %q: valid = %v, want %v (%v)", tc.name, got, tc.ok, diags)
		}
	}
}

// createDirect runs the resource's Create against c for a plan naming name and
// returns the response.
func createDirect(t *testing.T, c *client.Client, name string) *fwresource.CreateResponse {
	t.Helper()
	return createDirectCtx(t, context.Background(), c, name)
}

func createDirectCtx(t *testing.T, ctx context.Context, c *client.Client, name string) *fwresource.CreateResponse {
	t.Helper()
	s := serverSchema(t)
	typ := s.Type().TerraformType(ctx)
	unknown := tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	plan := tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(typ, map[string]tftypes.Value{
		"id": unknown, "name": tftypes.NewValue(tftypes.String, name),
		"agent_api_key": unknown, "install_command": unknown,
		"created_at": unknown, "created_via": unknown,
	})}
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(typ, nil)}}
	r := &serverResource{c: c}
	r.Create(ctx, fwresource.CreateRequest{Plan: plan}, resp)
	return resp
}

// R4: the lost-201 replay is adopted with exactly the warning the docs quote.
func TestServerCreateReplayWarning(t *testing.T) {
	f := fake.New(fake.Options{})
	t.Cleanup(f.Close)
	f.LoseNextCreateResponse()
	resp := createDirect(t, testFakeClient(t, f), "web")

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", resp.Diagnostics)
	}
	ws := resp.Diagnostics.Warnings()
	if len(ws) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(ws), resp.Diagnostics)
	}
	want := "server 1 was created but its agent key was lost in a retried request; rotate it in the dashboard or with POST /v1/servers/1/agent-key:rotate"
	if ws[0].Detail() != want {
		t.Errorf("warning detail = %q\nwant %q", ws[0].Detail(), want)
	}
	var m serverModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &m)...)
	if resp.Diagnostics.HasError() {
		t.Fatalf("reading state: %v", resp.Diagnostics)
	}
	if m.ID.ValueString() != "1" || m.Name.ValueString() != "web" {
		t.Errorf("state id %q name %q, want 1 web", m.ID.ValueString(), m.Name.ValueString())
	}
	if !m.AgentAPIKey.IsNull() || !m.InstallCommand.IsNull() {
		t.Errorf("agent_api_key / install_command must be null after a replay")
	}
	if n := len(f.Servers()); n != 1 {
		t.Errorf("%d servers exist, want 1", n)
	}
}

// A normal create sets no warning, and errors name the operation.
func TestServerCreateDiagnostics(t *testing.T) {
	f := fake.New(fake.Options{MaxServers: 1})
	t.Cleanup(f.Close)
	c := testFakeClient(t, f)

	ok := createDirect(t, c, "web-1")
	if len(ok.Diagnostics) != 0 {
		t.Fatalf("first create: %v", ok.Diagnostics)
	}
	var m serverModel
	ok.Diagnostics.Append(ok.State.Get(context.Background(), &m)...)
	if m.AgentAPIKey.ValueString() == "" || !strings.Contains(m.InstallCommand.ValueString(), m.AgentAPIKey.ValueString()) {
		t.Errorf("first create: key or install command missing from state")
	}

	limited := createDirect(t, c, "web-2")
	errs := limited.Diagnostics.Errors()
	if len(errs) != 1 {
		t.Fatalf("plan-limited create: %v", limited.Diagnostics)
	}
	if errs[0].Summary() != "Creating the server failed" {
		t.Errorf("summary = %q", errs[0].Summary())
	}
	wantDetail := regexp.MustCompile(`^Plan limit reached \(plan_limit_reached\): .* \[request_id req_fake_\d+\]\n\nRaise the plan limit at https://app\.monitorable\.io/billing or remove a server\.$`)
	if !wantDetail.MatchString(errs[0].Detail()) {
		t.Errorf("detail = %q", errs[0].Detail())
	}
	if !limited.State.Raw.IsNull() {
		t.Errorf("state written after a failed create")
	}
}

const orphanHintText = "The server may have been created before the request failed. " +
	"Check `data \"monitorable_servers\"` or the dashboard and `terraform import` it " +
	"before applying again, or a second server will be created."

// createError runs a Create that must fail and returns its one error.
func createError(t *testing.T, ctx context.Context, c *client.Client, name string) diag.Diagnostic {
	t.Helper()
	resp := createDirectCtx(t, ctx, c, name)
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 {
		t.Fatalf("want one error, got %v", resp.Diagnostics)
	}
	if errs[0].Summary() != "Creating the server failed" {
		t.Errorf("summary = %q", errs[0].Summary())
	}
	if !resp.State.Raw.IsNull() {
		t.Errorf("state written after a failed create")
	}
	return errs[0]
}

// A create that fails in a way that may still have created the server says
// so: a 5xx (a 500 is not retried), a transport error, a cancelled context.
func TestServerCreateMayHaveCreatedHint(t *testing.T) {
	t.Run("500", func(t *testing.T) {
		f := fake.New(fake.Options{})
		t.Cleanup(f.Close)
		f.InjectStatusOn("POST", "/v1/servers", 500, 1)
		d := createError(t, context.Background(), testFakeClient(t, f), "web")
		if !strings.HasPrefix(d.Detail(), "Internal error (internal): ") || !strings.HasSuffix(d.Detail(), "\n\n"+orphanHintText) {
			t.Errorf("detail = %q", d.Detail())
		}
		if n := len(createPosts(f)); n != 1 {
			t.Errorf("POST /v1/servers sent %d times, want 1 (a 500 is final)", n)
		}
	})
	t.Run("transport", func(t *testing.T) {
		f := fake.New(fake.Options{})
		c := testFakeClient(t, f)
		f.Close() // nothing listens any more: every attempt is a connection error
		d := createError(t, context.Background(), c, "web")
		if !strings.HasSuffix(d.Detail(), "\n\n"+orphanHintText) {
			t.Errorf("detail = %q", d.Detail())
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		f := fake.New(fake.Options{})
		t.Cleanup(f.Close)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		d := createError(t, ctx, testFakeClient(t, f), "web")
		if !strings.HasSuffix(d.Detail(), "\n\n"+orphanHintText) {
			t.Errorf("detail = %q", d.Detail())
		}
	})
}

// A 4xx is a refusal: nothing was created, so there is no hint.
func TestServerCreateRefusalHasNoHint(t *testing.T) {
	f := fake.New(fake.Options{})
	t.Cleanup(f.Close)
	// Bypasses the schema validators: the API itself answers 400.
	d := createError(t, context.Background(), testFakeClient(t, f), "")
	if !strings.HasPrefix(d.Detail(), "Invalid argument (invalid_argument): ") {
		t.Errorf("detail = %q, want the API's 400", d.Detail())
	}
	if strings.Contains(d.Detail(), "may have been created") {
		t.Errorf("a 400 got the may-have-been-created hint: %q", d.Detail())
	}
	if n := len(f.Servers()); n != 0 {
		t.Errorf("%d servers exist, want 0", n)
	}
}
