package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

const serversDSAddr = "data.monitorable_servers.test"

// TestServersDataSourceBasic runs on the fake and on a real endpoint: it
// creates one server, then finds it by a name_contains that only it matches.
func TestServersDataSourceBasic(t *testing.T) {
	testAccPreCheck(t)
	name := testAccName("ds")
	cfg := serverConfig(name) + fmt.Sprintf(`
data "monitorable_servers" "test" {
  name_contains = %q
  depends_on    = [monitorable_server.test]
}
`, name)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(serversDSAddr, "servers.#", "1"),
				resource.TestCheckResourceAttrPair(serversDSAddr, "servers.0.id", serverAddr, "id"),
				resource.TestCheckResourceAttr(serversDSAddr, "servers.0.name", name),
				resource.TestCheckResourceAttr(serversDSAddr, "servers.0.alert_template_id", "0"),
				resource.TestCheckResourceAttr(serversDSAddr, "servers.0.created_via", "api"),
			),
		}},
	})
}

func TestServersDataSourcePagesAndFilters(t *testing.T) {
	f, prov := newTestFake(t, fake.Options{})
	for i := 1; i <= 150; i++ {
		f.AddServer(fmt.Sprintf("srv-%03d", i))
	}
	queryOf := func() []string {
		var qs []string
		for _, r := range f.Requests() {
			if r.Method == "GET" && r.Path == "/v1/servers" {
				qs = append(qs, r.Query)
			}
		}
		return qs
	}
	hasQuery := func(sub ...string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			for _, q := range queryOf() {
				ok := true
				for _, s := range sub {
					ok = ok && strings.Contains(q, s)
				}
				if ok {
					return nil
				}
			}
			return fmt.Errorf("no GET /v1/servers with %v among %v", sub, queryOf())
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// 150 servers need two pages of 100: the cursor is followed.
				Config: prov + "data \"monitorable_servers\" \"test\" {\n}\n",
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(serversDSAddr, "servers.#", "150"),
					resource.TestCheckResourceAttr(serversDSAddr, "servers.0.name", "srv-001"),
					resource.TestCheckResourceAttr(serversDSAddr, "servers.149.name", "srv-150"),
					hasQuery("cursor="),
				),
			},
			{
				Config: prov + "data \"monitorable_servers\" \"test\" {\n  name_contains = \"SRV-14\"\n}\n",
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(serversDSAddr, "servers.#", "10"),
					hasQuery("q=SRV-14"),
				),
			},
			{
				Config: prov + "data \"monitorable_servers\" \"test\" {\n  status = \"offline\"\n}\n",
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(serversDSAddr, "servers.#", "150"),
					hasQuery("status=offline"),
				),
			},
			{
				// Nothing seeded is online: an empty match is an empty list.
				Config: prov + "data \"monitorable_servers\" \"test\" {\n  status = \"online\"\n}\n",
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(serversDSAddr, "servers.#", "0"),
					hasQuery("status=online"),
				),
			},
		},
	})
}

func TestServersDataSourceInvalidStatus(t *testing.T) {
	_, prov := newTestFake(t, fake.Options{})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      prov + "data \"monitorable_servers\" \"test\" {\n  status = \"healthy\"\n}\n",
			ExpectError: regexp.MustCompile(`(?s)online.*stale.*offline.*dormant`),
		}},
	})
}
