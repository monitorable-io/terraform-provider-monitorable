package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client/fake"
)

const (
	templateDSAddr  = "data.monitorable_alert_template.test"
	templatesDSAddr = "data.monitorable_alert_templates.test"
)

func templateByName(name string) string {
	return fmt.Sprintf("data \"monitorable_alert_template\" \"test\" {\n  name = %q\n}\n", name)
}

func templateByID(id string) string {
	return fmt.Sprintf("data \"monitorable_alert_template\" \"test\" {\n  id = %q\n}\n", id)
}

// TestAlertTemplateDataSourceByName relies on the fake's seeded "Strict".
func TestAlertTemplateDataSourceByName(t *testing.T) {
	_, prov := newTestFake(t, fake.Options{})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: prov + templateByName("Strict"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(templateDSAddr, "id", "2"),
					resource.TestCheckResourceAttr(templateDSAddr, "name", "Strict"),
					resource.TestCheckResourceAttr(templateDSAddr, "is_default", "false"),
				),
			},
			{
				Config: prov + templateByID("1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(templateDSAddr, "name", "Default"),
					resource.TestCheckResourceAttr(templateDSAddr, "is_default", "true"),
				),
			},
			// Case-sensitive, like the server lookup.
			{Config: prov + templateByName("strict"), ExpectError: regexp.MustCompile(`no\s+alert\s+template\s+named\s+"strict"`)},
			{Config: prov + templateByID("77"), ExpectError: regexp.MustCompile(`alert\s+template\s+77\s+not\s+found`)},
			{Config: prov + templateByID("two"), ExpectError: regexp.MustCompile(`id\s+must\s+be\s+a\s+decimal\s+integer`)},
			{
				Config:      prov + "data \"monitorable_alert_template\" \"test\" {\n  id   = \"1\"\n  name = \"Default\"\n}\n",
				ExpectError: regexp.MustCompile(`(?s)Exactly\s+one\s+of`),
			},
			{Config: prov + templateByID("1")}, // valid again, so the final destroy can plan
		},
	})
}

// TestAlertTemplateDataSourceDefaultByName also runs on a real endpoint: it
// asks the API for the default template's name and looks that name up.
func TestAlertTemplateDataSourceDefaultByName(t *testing.T) {
	testAccPreCheck(t)
	templates, err := testAccClient(t).ListAlertTemplates(context.Background())
	if err != nil {
		t.Fatalf("listing alert templates: %v", err)
	}
	var def, sameName int
	var name string
	for _, tpl := range templates {
		if tpl.IsDefault {
			def++
			name = tpl.Name
		}
	}
	if def != 1 {
		t.Skipf("expected exactly one default template, found %d", def)
	}
	for _, tpl := range templates {
		if tpl.Name == name {
			sameName++
		}
	}
	if sameName != 1 {
		t.Skipf("the default template's name %q is not unique", name)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: templateByName(name),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(templateDSAddr, "name", name),
				resource.TestCheckResourceAttr(templateDSAddr, "is_default", "true"),
			),
		}},
	})
}

func TestAlertTemplatesDataSource(t *testing.T) {
	_, prov := newTestFake(t, fake.Options{})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: prov + "data \"monitorable_alert_templates\" \"test\" {\n}\n",
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(templatesDSAddr, "templates.#", "2"),
				resource.TestCheckResourceAttr(templatesDSAddr, "templates.0.id", "1"),
				resource.TestCheckResourceAttr(templatesDSAddr, "templates.0.name", "Default"),
				resource.TestCheckResourceAttr(templatesDSAddr, "templates.0.is_default", "true"),
				resource.TestCheckResourceAttr(templatesDSAddr, "templates.1.id", "2"),
				resource.TestCheckResourceAttr(templatesDSAddr, "templates.1.name", "Strict"),
				resource.TestCheckResourceAttr(templatesDSAddr, "templates.1.is_default", "false"),
			),
		}},
	})
}
