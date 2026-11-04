package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

func TestProviderMetadata(t *testing.T) {
	p := New("1.2.3")()
	resp := &fwprovider.MetadataResponse{}
	p.Metadata(context.Background(), fwprovider.MetadataRequest{}, resp)
	if resp.TypeName != "monitorable" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "monitorable")
	}
	if resp.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", resp.Version, "1.2.3")
	}
}

func TestProviderServes(t *testing.T) {
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatalf("NewProtocol6WithError: %v", err)
	}
	if server == nil {
		t.Fatal("protocol 6 server is nil")
	}
}

// configure runs the provider's Configure with the given attribute values (nil
// = null) and returns the response.
func configure(t *testing.T, version string, apiKey, endpoint *string) *fwprovider.ConfigureResponse {
	t.Helper()
	ctx := context.Background()
	p := New(version)()
	sresp := &fwprovider.SchemaResponse{}
	p.Schema(ctx, fwprovider.SchemaRequest{}, sresp)

	str := func(s *string) tftypes.Value {
		if s == nil {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, *s)
	}
	raw := tftypes.NewValue(sresp.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
		"api_key": str(apiKey), "endpoint": str(endpoint),
	})
	resp := &fwprovider.ConfigureResponse{}
	p.Configure(ctx, fwprovider.ConfigureRequest{Config: tfsdk.Config{Schema: sresp.Schema, Raw: raw}}, resp)
	return resp
}

func ptr(s string) *string { return &s }

func TestProviderConfigureOK(t *testing.T) {
	f := fake.New(fake.Options{})
	defer f.Close()

	resp := configure(t, "1.2.3", ptr("mon_test_fake-unit"), ptr(f.URL()))
	if resp.Diagnostics.HasError() || len(resp.Diagnostics) != 0 {
		t.Fatalf("diagnostics: %v", resp.Diagnostics)
	}
	if _, ok := resp.ResourceData.(*client.Client); !ok {
		t.Errorf("ResourceData = %T, want *client.Client", resp.ResourceData)
	}
	if resp.DataSourceData != resp.ResourceData {
		t.Errorf("DataSourceData and ResourceData differ")
	}
}

func TestProviderConfigureEnvFallbackAndPrecedence(t *testing.T) {
	good := fake.New(fake.Options{})
	defer good.Close()
	other := fake.New(fake.Options{})
	other.Close() // the attribute must win: a dead endpoint here would fail the call

	// Both attributes null: both come from the environment.
	t.Setenv("MONITORABLE_API_KEY", "mon_test_fake-env")
	t.Setenv("MONITORABLE_ENDPOINT", good.URL())
	if resp := configure(t, "x", nil, nil); resp.Diagnostics.HasError() {
		t.Fatalf("env fallback: %v", resp.Diagnostics)
	}
	// An empty attribute counts as unset.
	if resp := configure(t, "x", ptr(""), ptr("")); resp.Diagnostics.HasError() {
		t.Fatalf("empty attributes: %v", resp.Diagnostics)
	}
	// A non-empty attribute beats the environment: a wrong env key is ignored...
	t.Setenv("MONITORABLE_API_KEY", "mon_test_wrong")
	if resp := configure(t, "x", ptr("mon_test_fake-attr"), nil); resp.Diagnostics.HasError() {
		t.Fatalf("api_key attribute should win: %v", resp.Diagnostics)
	}
	// ... and so is a dead env endpoint.
	t.Setenv("MONITORABLE_ENDPOINT", other.URL())
	if resp := configure(t, "x", ptr("mon_test_fake-attr"), ptr(good.URL())); resp.Diagnostics.HasError() {
		t.Fatalf("endpoint attribute should win: %v", resp.Diagnostics)
	}
}

func TestProviderConfigureMissingKeyUnit(t *testing.T) {
	t.Setenv("MONITORABLE_API_KEY", "")
	resp := configure(t, "x", nil, nil)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	got := resp.Diagnostics.Errors()[0].Detail()
	if want := "api_key is not set: set it in the provider block or in MONITORABLE_API_KEY"; got != want {
		t.Errorf("detail = %q, want %q", got, want)
	}
}

func TestProviderConfigureUnknownValues(t *testing.T) {
	ctx := context.Background()
	p := New("x")()
	sresp := &fwprovider.SchemaResponse{}
	p.Schema(ctx, fwprovider.SchemaRequest{}, sresp)
	for _, attr := range []string{"api_key", "endpoint"} {
		vals := map[string]tftypes.Value{
			"api_key":  tftypes.NewValue(tftypes.String, "mon_test_fake-x"),
			"endpoint": tftypes.NewValue(tftypes.String, "http://127.0.0.1:1"),
		}
		vals[attr] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
		resp := &fwprovider.ConfigureResponse{}
		p.Configure(ctx, fwprovider.ConfigureRequest{Config: tfsdk.Config{
			Schema: sresp.Schema, Raw: tftypes.NewValue(sresp.Schema.Type().TerraformType(ctx), vals),
		}}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("unknown %s: want a diagnostic", attr)
		}
	}
}

func TestProviderConfigureBadKeyUnit(t *testing.T) {
	f := fake.New(fake.Options{})
	defer f.Close()
	resp := configure(t, "x", ptr("mon_test_wrong"), ptr(f.URL()))
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	d := resp.Diagnostics.Errors()[0]
	if d.Summary() != "Invalid Monitorable API key" || !strings.Contains(d.Detail(), "invalid_api_key") {
		t.Errorf("diagnostic = %q / %q", d.Summary(), d.Detail())
	}
	if resp.ResourceData != nil {
		t.Errorf("ResourceData = %v, want nil", resp.ResourceData)
	}
	if strings.Contains(fmt.Sprint(resp.Diagnostics), "mon_test_wrong") {
		t.Error("the key leaked into a diagnostic")
	}
}

func TestProviderConfigurePlanGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"title":"Forbidden","code":"api_access_not_in_plan","detail":"API access is not part of the Free plan"}`))
	}))
	defer srv.Close()
	resp := configure(t, "x", ptr("mon_test_fake-x"), ptr(srv.URL))
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "API access is not in your plan" {
		t.Errorf("summary = %q", got)
	}
}

func TestProviderConfigureUnreachable(t *testing.T) {
	f := fake.New(fake.Options{})
	url := f.URL()
	f.Close()
	resp := configure(t, "x", ptr("mon_test_fake-secret-key"), ptr(url))
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	if got, want := resp.Diagnostics.Errors()[0].Summary(), "Cannot reach the Monitorable API at "+url; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if strings.Contains(fmt.Sprint(resp.Diagnostics), "mon_test_fake-secret-key") {
		t.Error("the key leaked into a diagnostic")
	}
}

func TestProviderConfigureInvalidEndpoint(t *testing.T) {
	resp := configure(t, "x", ptr("mon_test_fake-x"), ptr("https://api.example.com/v1"))
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
}

func TestProviderConfigureUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key_id":1,"key_name":"k","scopes":["servers:read"],"role":"admin","organization_id":1,"organization_name":"o","plan_name":"Pro","expires_at":null}`))
	}))
	defer srv.Close()
	resp := configure(t, "1.2.3", ptr("mon_test_fake-x"), ptr(srv.URL))
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if want := "terraform-provider-monitorable/1.2.3 (+https://monitorable.io/docs/api/terraform/)"; ua != want {
		t.Errorf("User-Agent = %q, want %q", ua, want)
	}
}

func TestProviderSchema(t *testing.T) {
	sresp := &fwprovider.SchemaResponse{}
	New("x")().Schema(context.Background(), fwprovider.SchemaRequest{}, sresp)
	key, ok := sresp.Schema.Attributes["api_key"]
	if !ok || !key.IsSensitive() || !key.IsOptional() {
		t.Errorf("api_key must be optional and sensitive: %v", key)
	}
	ep, ok := sresp.Schema.Attributes["endpoint"]
	if !ok || !ep.IsOptional() || ep.IsSensitive() {
		t.Errorf("endpoint must be optional and not sensitive: %v", ep)
	}
	p := New("x")()
	// The five data sources; the test-only monitorable_test_me is not shipped.
	if n := len(p.DataSources(context.Background())); n != 5 {
		t.Errorf("shipped DataSources = %d, want 5", n)
	}
}

// TestProviderConfigureMissingKey needs Terraform to configure the provider,
// which it only does for a configuration that uses a data source or resource:
// monitorable_test_me is the test-only data source for that.
func TestProviderConfigureMissingKey(t *testing.T) {
	t.Setenv("MONITORABLE_API_KEY", "")
	t.Setenv("MONITORABLE_ENDPOINT", "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      `data "monitorable_test_me" "x" {}`,
			ExpectError: regexp.MustCompile(`api_key is not set`),
		}},
	})
}

func TestProviderConfigureBadKey(t *testing.T) {
	f, _ := newTestFake(t, fake.Options{})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      providerBlock(f.URL(), "mon_test_wrong") + `data "monitorable_test_me" "x" {}`,
			ExpectError: regexp.MustCompile(`Invalid Monitorable API key`),
		}},
	})
}

// TestProviderConfigureAcceptance is the happy path through Terraform: the
// provider configures from the environment and Me answers.
func TestProviderConfigureAcceptance(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `data "monitorable_test_me" "x" {}`,
			Check:  resource.TestMatchResourceAttr("data.monitorable_test_me.x", "organization_id", regexp.MustCompile(`^[1-9][0-9]*$`)),
		}},
	})
}

func TestProviderConfigureTrimsAPIKey(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key_id":1,"key_name":"k","scopes":[],"role":"admin","organization_id":1,"organization_name":"o","plan_name":"Pro","expires_at":null}`))
	}))
	defer srv.Close()

	// Attribute.
	t.Setenv("MONITORABLE_API_KEY", "")
	if resp := configure(t, "x", ptr(" mon_test_fake-trim\n"), ptr(srv.URL)); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if auth != "Bearer mon_test_fake-trim" {
		t.Errorf("attribute: Authorization = %q", auth)
	}
	// Environment (a trailing newline from `export K=$(cat file)` is the usual cause).
	t.Setenv("MONITORABLE_API_KEY", "mon_test_fake-env\n")
	if resp := configure(t, "x", nil, ptr(srv.URL)); resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if auth != "Bearer mon_test_fake-env" {
		t.Errorf("env: Authorization = %q", auth)
	}
	// Whitespace only is no key.
	t.Setenv("MONITORABLE_API_KEY", " \n")
	resp := configure(t, "x", ptr("  "), ptr(srv.URL))
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "api_key is not set") {
		t.Errorf("whitespace-only key: %v", resp.Diagnostics)
	}
}

func TestProviderConfigureNonTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	resp := configure(t, "x", ptr("mon_test_fake-x"), ptr(srv.URL))
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	if got := resp.Diagnostics.Errors()[0].Summary(); got != "Monitorable API request failed" {
		t.Errorf("summary = %q, want %q (a bad response is not unreachability)", got, "Monitorable API request failed")
	}
}

func TestCheckRealEndpointEnv(t *testing.T) {
	if err := checkRealEndpointEnv("k", ""); err == nil || !strings.Contains(err.Error(), "MONITORABLE_ENDPOINT must be set when MONITORABLE_API_KEY is set") {
		t.Errorf("key without endpoint: %v", err)
	}
	for _, c := range [][2]string{{"k", "https://x"}, {"", ""}, {"", "https://x"}} {
		if err := checkRealEndpointEnv(c[0], c[1]); err != nil {
			t.Errorf("checkRealEndpointEnv(%q, %q) = %v", c[0], c[1], err)
		}
	}
}

// An http:// endpoint on a host that is not local is allowed, with a warning
// that the key travels unencrypted. The .invalid name never resolves, so the
// configuration then fails on the network: the warning comes first.
func TestProviderConfigurePlaintextEndpointWarns(t *testing.T) {
	resp := configure(t, "x", ptr("mon_test_fake-secret-key"), ptr("http://api.monitorable.invalid"))
	ws := resp.Diagnostics.Warnings()
	if len(ws) != 1 {
		t.Fatalf("warnings = %v, want one", ws)
	}
	if got, want := ws[0].Detail(), "the API key is sent unencrypted to api.monitorable.invalid; use https"; got != want {
		t.Errorf("warning = %q, want %q", got, want)
	}
	if strings.Contains(fmt.Sprint(resp.Diagnostics), "mon_test_fake-secret-key") {
		t.Error("the key leaked into a diagnostic")
	}
}

// A loopback or .lan/.local http:// endpoint (a development stack) is not warned about.
func TestProviderConfigureLocalPlaintextNoWarning(t *testing.T) {
	f := fake.New(fake.Options{})
	defer f.Close()
	resp := configure(t, "x", ptr("mon_test_fake-unit"), ptr(f.URL()))
	if len(resp.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %v, want none for %s", resp.Diagnostics, f.URL())
	}
}
