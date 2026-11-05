package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

const serverDSAddr = "data.monitorable_server.test"

func serverByID(id string) string {
	return fmt.Sprintf("data \"monitorable_server\" \"test\" {\n  id = %q\n}\n", id)
}

func serverByName(name string) string {
	return fmt.Sprintf("data \"monitorable_server\" \"test\" {\n  name = %q\n}\n", name)
}

func TestServerDataSourceByID(t *testing.T) {
	f, prov := newTestFake(t, fake.Options{})
	f.AddServer("web-01")
	id := f.AddServer("db-01")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: prov + serverByID(idString(id)),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(serverDSAddr, "id", "2"),
				resource.TestCheckResourceAttr(serverDSAddr, "name", "db-01"),
				resource.TestCheckResourceAttr(serverDSAddr, "status", "offline"),
				resource.TestCheckResourceAttr(serverDSAddr, "triage", "unreachable"),
				resource.TestCheckResourceAttr(serverDSAddr, "alert_template_id", "0"),
				resource.TestCheckResourceAttr(serverDSAddr, "alerts_muted", "false"),
				resource.TestCheckNoResourceAttr(serverDSAddr, "alerts_muted_until"),
				resource.TestCheckNoResourceAttr(serverDSAddr, "last_seen_at"),
				resource.TestCheckResourceAttr(serverDSAddr, "open_incidents", "0"),
				resource.TestCheckResourceAttr(serverDSAddr, "unacked_firing", "0"),
				resource.TestCheckResourceAttr(serverDSAddr, "uptime_seconds", "0"),
				resource.TestCheckResourceAttr(serverDSAddr, "created_via", "dashboard"),
				resource.TestMatchResourceAttr(serverDSAddr, "created_at", regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)),
			),
		}},
	})
}

func TestServerDataSourceByName(t *testing.T) {
	f, prov := newTestFake(t, fake.Options{})
	f.AddServer("web-01")
	f.AddServer("web-010")
	f.AddServer("db-01")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			// web-01 is also a substring of web-010: the exact name wins.
			Config: prov + serverByName("web-01"),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(serverDSAddr, "id", "1"),
				resource.TestCheckResourceAttr(serverDSAddr, "name", "web-01"),
			),
		}},
	})
}

func TestServerDataSourceAmbiguousName(t *testing.T) {
	f, prov := newTestFake(t, fake.Options{})
	f.AddServer("web")
	f.AddServer("db")
	f.AddServer("web")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      prov + serverByName("web"),
			ExpectError: regexp.MustCompile(`2\s+servers\s+are\s+named\s+"web"\s+\(ids\s+1,\s+3\);\s+look\s+it\s+up\s+by\s+id`),
		}},
	})
}

func TestServerDataSourceNameNotFound(t *testing.T) {
	f, prov := newTestFake(t, fake.Options{})
	f.AddServer("web-01")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: prov + serverByName("db-01"), ExpectError: regexp.MustCompile(`no\s+server\s+named\s+"db-01"`)},
			// Matching is case-sensitive even though ?q= is not.
			{Config: prov + serverByName("WEB-01"), ExpectError: regexp.MustCompile(`no\s+server\s+named\s+"WEB-01"`)},
		},
	})
}

func TestServerDataSourceIDNotFound(t *testing.T) {
	_, prov := newTestFake(t, fake.Options{})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      prov + serverByID("999"),
			ExpectError: regexp.MustCompile(`server\s+999\s+not\s+found`),
		}},
	})
}

func TestServerDataSourceInvalidID(t *testing.T) {
	_, prov := newTestFake(t, fake.Options{})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      prov + serverByID("web-1"),
			ExpectError: regexp.MustCompile(`id\s+must\s+be\s+a\s+decimal\s+integer,\s+got\s+"web-1"`),
		}},
	})
}

func TestServerDataSourceIDAndNameBothRefused(t *testing.T) {
	f, prov := newTestFake(t, fake.Options{})
	f.AddServer("web")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      prov + "data \"monitorable_server\" \"test\" {\n  id   = \"1\"\n  name = \"web\"\n}\n",
				ExpectError: regexp.MustCompile(`(?s)Exactly\s+one\s+of`),
			},
			{
				Config:      prov + "data \"monitorable_server\" \"test\" {\n}\n",
				ExpectError: regexp.MustCompile(`(?s)Exactly\s+one\s+of`),
			},
			{Config: prov + serverByName("web")}, // valid again, so the final destroy can plan
		},
	})
}
