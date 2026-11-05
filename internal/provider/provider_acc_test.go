package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

// accKey is the API key of the shared fake and of every newTestFake. The fake
// accepts any key that starts with mon_test_fake.
const accKey = "mon_test_fake-acc"

// accFake is the process-wide fake API behind the acceptance suite. TestMain
// starts it when MONITORABLE_API_KEY is unset. It is nil when the variable was
// set before the run: the suite then talks to the real endpoint
// (MONITORABLE_ENDPOINT, required), and tests that need fake injection call
// skipIfRealEndpoint (or newTestFake, which does).
var accFake *fake.Fake

var logRealTargetOnce sync.Once

// testAccProtoV6ProviderFactories serves the provider under test. It wraps the
// shipped provider with the test-only data source monitorable_test_me (see
// testProvider); nothing here is part of the released provider.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"monitorable": providerserver.NewProtocol6WithError(&testProvider{Provider: New("test")()}),
}

// TestMain starts the shared fake (MaxServers 10, default scopes) before any
// test runs, when MONITORABLE_API_KEY is unset, and points MONITORABLE_ENDPOINT
// and MONITORABLE_API_KEY at it for the process. Starting it here, not lazily,
// means no test's t.Setenv cleanup can leave those variables unset for later
// tests. Tests that call t.Setenv on MONITORABLE_API_KEY or MONITORABLE_ENDPOINT
// must not run in parallel.
func TestMain(m *testing.M) {
	if os.Getenv("MONITORABLE_API_KEY") == "" {
		accFake = fake.New(fake.Options{MaxServers: 10})
		os.Setenv("MONITORABLE_ENDPOINT", accFake.URL())
		os.Setenv("MONITORABLE_API_KEY", accKey)
	}
	code := m.Run()
	if accFake != nil {
		accFake.Close()
	}
	os.Exit(code)
}

// checkRealEndpointEnv is the real-endpoint guard: a key without an endpoint
// would send acceptance tests to production.
func checkRealEndpointEnv(key, endpoint string) error {
	if key != "" && endpoint == "" {
		return errors.New("MONITORABLE_ENDPOINT must be set when MONITORABLE_API_KEY is set: acceptance tests never default to production")
	}
	return nil
}

// testAccPreCheck is the PreCheck of every acceptance test that can also run on
// a real endpoint. Against the shared fake it does nothing more. Against a real
// endpoint (MONITORABLE_API_KEY set before the run) it fails the test unless
// MONITORABLE_ENDPOINT is set too, and logs the target host once, never the
// key. Use it for tests that need nothing but a working API; for special
// Options, seeded data or exact counts use newTestFake instead.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	if accFake != nil {
		return
	}
	endpoint := os.Getenv("MONITORABLE_ENDPOINT")
	if err := checkRealEndpointEnv(os.Getenv("MONITORABLE_API_KEY"), endpoint); err != nil {
		t.Fatal(err)
	}
	logRealTargetOnce.Do(func() {
		host := endpoint
		if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
			host = u.Host
		}
		t.Logf("acceptance tests run against the real endpoint %s", host)
	})
}

// testAccName returns a unique resource name, tfacc-<prefix>-<unix nanos>, so
// runs against a real endpoint never collide with each other or with existing
// servers. Every acceptance test that creates something uses it.
func testAccName(prefix string) string {
	return fmt.Sprintf("tfacc-%s-%d", prefix, time.Now().UnixNano())
}

// skipIfRealEndpoint skips a test that needs fake-only behaviour (error
// injection, a clock, deleting a server behind Terraform's back, exact
// counts). Call it first, before any other setup.
func skipIfRealEndpoint(t *testing.T) {
	t.Helper()
	testAccPreCheck(t)
	if accFake == nil {
		t.Skip("needs the in-process fake API; MONITORABLE_API_KEY selects a real endpoint")
	}
}

// newTestFake starts a dedicated fake with opts (closed when the test ends) and
// returns it with an HCL provider block that points the provider at it. The
// block wins over the environment, so it works next to the shared fake. Use it
// for tests that need special Options, seeded data or exact counts; it skips
// the test on a real endpoint. Prepend the block to the test's configuration.
func newTestFake(t *testing.T, opts fake.Options) (*fake.Fake, string) {
	t.Helper()
	skipIfRealEndpoint(t)
	f := fake.New(opts)
	t.Cleanup(f.Close)
	return f, providerBlock(f.URL(), accKey+"-"+t.Name())
}

// providerBlock renders an explicit provider configuration.
func providerBlock(endpoint, key string) string {
	return fmt.Sprintf("provider \"monitorable\" {\n  endpoint = %q\n  api_key  = %q\n}\n", endpoint, key)
}

// testAccClient returns a client for the environment the shared fake or the
// real endpoint set up. Use it in CheckDestroy and other out-of-band checks
// (GetServer, IsNotFound). Call it after testAccPreCheck.
func testAccClient(t *testing.T) *client.Client {
	t.Helper()
	testAccPreCheck(t)
	c, err := client.New(os.Getenv("MONITORABLE_ENDPOINT"), os.Getenv("MONITORABLE_API_KEY"), "")
	if err != nil {
		t.Fatalf("building the acceptance client: %v", err)
	}
	return c
}

// testProvider is the shipped provider plus test-only data sources. Terraform
// does not configure a provider that no resource or data source uses, so the
// Configure acceptance tests need one; it lives here, in a _test.go file, so
// the released provider gains nothing.
type testProvider struct {
	fwprovider.Provider
}

func (p *testProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return append(p.Provider.DataSources(ctx), func() datasource.DataSource { return &testMeDataSource{} })
}

// testMeDataSource is monitorable_test_me: it calls GET /v1/me and exposes the
// organization id. It forces Terraform to configure the provider.
type testMeDataSource struct {
	c *client.Client
}

func (d *testMeDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_test_me"
}

func (d *testMeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"organization_id": schema.StringAttribute{Computed: true},
	}}
}

func (d *testMeDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	if c, ok := req.ProviderData.(*client.Client); ok {
		d.c = c
	}
}

func (d *testMeDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	me, err := d.c.Me(ctx)
	if err != nil {
		resp.Diagnostics.AddError("monitorable_test_me", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), types.StringValue(idString(me.OrganizationID)))...)
}
