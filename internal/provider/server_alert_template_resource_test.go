package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
)

const tmplAddr = "monitorable_server_alert_template.test"

// tmplConfig is a server and an alert template assignment on it.
func tmplConfig(serverName, templateID string) string {
	return serverConfig(serverName) + fmt.Sprintf(`
resource "monitorable_server_alert_template" "test" {
  server_id   = monitorable_server.test.id
  template_id = %q
}
`, templateID)
}

// tmplConfigFor assigns a template to a server that already exists.
func tmplConfigFor(serverID int64, templateID string) string {
	return fmt.Sprintf(`
resource "monitorable_server_alert_template" "test" {
  server_id   = %q
  template_id = %q
}
`, idString(serverID), templateID)
}

// seedServer makes a server outside Terraform (on the fake or the real
// endpoint) and deletes it when the test ends.
func seedServer(t *testing.T, prefix string) int64 {
	t.Helper()
	testAccPreCheck(t)
	c := testAccClient(t)
	out, err := c.CreateServer(context.Background(), testAccName(prefix), uuid.NewString())
	if err != nil {
		t.Fatalf("creating the server to adopt: %v", err)
	}
	id := out.Server.ID
	t.Cleanup(func() {
		if err := c.DeleteServer(context.Background(), id); err != nil && !client.IsNotFound(err) {
			t.Errorf("deleting server %d: %v", id, err)
		}
	})
	return id
}

// serverIDOf reads the server_id attribute of a child resource from the state.
func serverIDOf(s *terraform.State, addr string) (int64, error) {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return 0, fmt.Errorf("%s not in state", addr)
	}
	var id int64
	if _, err := fmt.Sscanf(rs.Primary.Attributes["server_id"], "%d", &id); err != nil {
		return 0, fmt.Errorf("%s server_id %q: %w", addr, rs.Primary.Attributes["server_id"], err)
	}
	return id, nil
}

// checkServerTemplate asserts the API shows templateID on the server of addr.
func checkServerTemplate(t *testing.T, addr, templateID string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id, err := serverIDOf(s, addr)
		if err != nil {
			return err
		}
		srv, err := testAccClient(t).GetServer(context.Background(), id)
		if err != nil {
			return err
		}
		if got := idString(srv.AlertTemplateID); got != templateID {
			return fmt.Errorf("server %d has alert_template_id %s, want %s", id, got, templateID)
		}
		return nil
	}
}

// templatePair picks the template ids the assign test uses: the first
// non-default template (or the default's own id when it is the only one) and,
// when there is more than one template, the default to move back to. The fake
// seeds 1 Default and 2 Strict.
func templatePair(t *testing.T) (first, second string) {
	t.Helper()
	if accFake != nil {
		return "2", "1"
	}
	list, err := testAccClient(t).ListAlertTemplates(context.Background())
	if err != nil || len(list) == 0 {
		t.Fatalf("listing alert templates: %v (%d found)", err, len(list))
	}
	var def, other int64
	for _, tpl := range list {
		if tpl.IsDefault && def == 0 {
			def = tpl.ID
		}
		if !tpl.IsDefault && other == 0 {
			other = tpl.ID
		}
	}
	switch {
	case other == 0:
		return idString(list[0].ID), ""
	case def == 0:
		return idString(other), idString(list[0].ID)
	}
	return idString(other), idString(def)
}

func TestAlertTemplateAssignReassignDestroy(t *testing.T) {
	testAccPreCheck(t)
	first, second := templatePair(t)
	name := testAccName("tmpl")
	var serverID string
	steps := []resource.TestStep{{
		Config: tmplConfig(name, first),
		Check: resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(tmplAddr, "template_id", first),
			resource.TestCheckResourceAttrPair(tmplAddr, "id", serverAddr, "id"),
			resource.TestCheckResourceAttrPair(tmplAddr, "server_id", serverAddr, "id"),
			checkServerTemplate(t, tmplAddr, first),
			captureID(serverAddr, &serverID),
		),
	}}
	if second != "" {
		steps = append(steps, resource.TestStep{
			Config: tmplConfig(name, second),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(tmplAddr, plancheck.ResourceActionUpdate)},
			},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(tmplAddr, "template_id", second),
				checkServerTemplate(t, tmplAddr, second),
				func(s *terraform.State) error {
					if id := s.RootModule().Resources[tmplAddr].Primary.ID; id != serverID {
						return fmt.Errorf("id changed on reassign: %s -> %s", serverID, id)
					}
					return nil
				},
			),
		})
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps:                    steps,
	})
}

// Destroying the assignment returns the server to the organization's default
// template (PUT 0). The server outlives the resource here, so the API can be
// asked afterwards.
func TestAlertTemplateDestroyResetsToDefault(t *testing.T) {
	skipIfRealEndpoint(t)
	id := seedServer(t, "tmpl-reset")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			srv, err := testAccClient(t).GetServer(context.Background(), id)
			if err != nil {
				return err
			}
			if srv.AlertTemplateID != 0 {
				return fmt.Errorf("server %d still has template %d after destroy; want 0", id, srv.AlertTemplateID)
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: tmplConfigFor(id, "2"),
			Check:  checkServerTemplateOfID(t, id, "2"),
		}},
	})
}

func checkServerTemplateOfID(t *testing.T, id int64, templateID string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		srv, err := testAccClient(t).GetServer(context.Background(), id)
		if err != nil {
			return err
		}
		if got := idString(srv.AlertTemplateID); got != templateID {
			return fmt.Errorf("server %d has alert_template_id %s, want %s", id, got, templateID)
		}
		return nil
	}
}

// Someone resets the assignment in the dashboard: the next plan re-assigns it.
func TestAlertTemplateDriftResetInDashboard(t *testing.T) {
	skipIfRealEndpoint(t)
	name := testAccName("tmpl-drift")
	var serverID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{
				Config: tmplConfig(name, "2"),
				Check:  captureID(serverAddr, &serverID),
			},
			{
				PreConfig: func() {
					var n int64
					fmt.Sscanf(serverID, "%d", &n)
					accFake.SetTemplate(n, 0)
				},
				Config:             tmplConfig(name, "2"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectResourceAction(tmplAddr, plancheck.ResourceActionCreate)},
				},
			},
			{
				// Applying puts it back.
				Config: tmplConfig(name, "2"),
				Check:  checkServerTemplate(t, tmplAddr, "2"),
			},
		},
	})
}

// A template assigned outside Terraform imports by the server id.
func TestAlertTemplateImport(t *testing.T) {
	testAccPreCheck(t)
	first, _ := templatePair(t)
	name := testAccName("tmpl-import")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{
			{Config: tmplConfig(name, first)}, // assigns first
			{
				Config:            tmplConfig(name, first),
				ResourceName:      tmplAddr,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// A server that follows the default template has no assignment to import.
func TestAlertTemplateImportDefaultRefused(t *testing.T) {
	id := seedServer(t, "tmpl-import-default")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        tmplConfigFor(id, "2"),
			ResourceName:  tmplAddr,
			ImportState:   true,
			ImportStateId: idString(id),
			ExpectError: regexp.MustCompile(fmt.Sprintf(
				`server\s+%d\s+follows\s+the\s+organization's\s+default\s+template;\s+there\s+is\s+no\s+assignment\s+to\s+import`, id)),
		}},
	})
}

func TestAlertTemplateImportInvalidID(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        tmplConfigFor(1, "2"),
			ResourceName:  tmplAddr,
			ImportState:   true,
			ImportStateId: "web-1",
			ExpectError:   regexp.MustCompile(`id must be a decimal integer, got "web-1"`),
		}},
	})
}

func TestAlertTemplateZeroRefused(t *testing.T) {
	testAccPreCheck(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      tmplConfigFor(1, "0"),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`destroy\s+the\s+resource\s+to\s+return\s+the\s+server\s+to\s+the\s+organization's\s+default\s+template`),
		}},
	})
}

func TestAlertTemplateIDsMustBeDecimal(t *testing.T) {
	testAccPreCheck(t)
	bad := regexp.MustCompile(`id\s+must\s+be\s+a\s+decimal\s+integer`)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      tmplConfigFor(1, "strict"),
				PlanOnly:    true,
				ExpectError: bad,
			},
			{
				Config: `resource "monitorable_server_alert_template" "test" {
  server_id   = "web-1"
  template_id = "2"
}
`,
				PlanOnly:    true,
				ExpectError: bad,
			},
		},
	})
}

// An unknown template id is the API's 404; nothing is written to state.
func TestAlertTemplateUnknownTemplateDiagnostic(t *testing.T) {
	skipIfRealEndpoint(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckServerDestroyed(testAccClient(t)),
		Steps: []resource.TestStep{{
			Config:      tmplConfig(testAccName("tmpl-unknown"), "999"),
			ExpectError: regexp.MustCompile(`Assigning\s+the\s+alert\s+template\s+failed[\s\S]*alert\s+template\s+not\s+found`),
		}},
	})
}
