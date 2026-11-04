package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// allDataSources are the constructors Task 5 ships.
func allDataSources() map[string]func() datasource.DataSource {
	return map[string]func() datasource.DataSource{
		"monitorable_server":                NewServerDataSource,
		"monitorable_servers":               NewServersDataSource,
		"monitorable_alert_template":        NewAlertTemplateDataSource,
		"monitorable_alert_templates":       NewAlertTemplatesDataSource,
		"monitorable_notification_channels": NewNotificationChannelsDataSource,
	}
}

func dataSourceSchema(t *testing.T, ctor func() datasource.DataSource) dsschema.Schema {
	t.Helper()
	var resp datasource.SchemaResponse
	ctor().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// walkAttributes calls fn for every attribute, nested ones included, with its
// dotted path.
func walkAttributes(prefix string, attrs map[string]dsschema.Attribute, fn func(path string, a dsschema.Attribute)) {
	for name, a := range attrs {
		p := prefix + name
		fn(p, a)
		if n, ok := a.(dsschema.ListNestedAttribute); ok {
			walkAttributes(p+".", n.NestedObject.Attributes, fn)
		}
	}
}

func TestDataSourcesAreShipped(t *testing.T) {
	shipped := map[string]bool{}
	p := New("test")()
	for _, ctor := range p.DataSources(context.Background()) {
		var resp datasource.MetadataResponse
		ctor().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "monitorable"}, &resp)
		shipped[resp.TypeName] = true
	}
	for name := range allDataSources() {
		if !shipped[name] {
			t.Errorf("%s is not in the provider's DataSources()", name)
		}
	}
	if len(shipped) != len(allDataSources()) {
		t.Errorf("provider ships %d data sources, want %d", len(shipped), len(allDataSources()))
	}
}

func TestDataSourceSchemasAreDocumented(t *testing.T) {
	for name, ctor := range allDataSources() {
		s := dataSourceSchema(t, ctor)
		if s.MarkdownDescription == "" {
			t.Errorf("%s has no MarkdownDescription", name)
		}
		walkAttributes("", s.Attributes, func(path string, a dsschema.Attribute) {
			if a.GetMarkdownDescription() == "" {
				t.Errorf("%s.%s has no MarkdownDescription", name, path)
			}
		})
	}
}

// serverAttrTypes is the Terraform type of one server object, derived from the
// shared schema the two server data sources build on.
var serverAttrTypes = func() map[string]attr.Type {
	obj := dsschema.NestedAttributeObject{Attributes: serverComputedAttributes()}.Type().(types.ObjectType)
	return obj.AttrTypes
}()

// TestServerAttrTypesMatchSchema pins that the server object is defined once:
// monitorable_server and the servers[*] elements carry the same attributes.
func TestServerAttrTypesMatchSchema(t *testing.T) {
	one := dataSourceSchema(t, NewServerDataSource)
	many := dataSourceSchema(t, NewServersDataSource)
	nested, ok := many.Attributes["servers"].(dsschema.ListNestedAttribute)
	if !ok {
		t.Fatal("servers is not a list nested attribute")
	}
	if len(nested.NestedObject.Attributes) != len(serverAttrTypes) || len(one.Attributes) != len(serverAttrTypes) {
		t.Fatalf("attribute counts differ: server %d, servers[*] %d, serverAttrTypes %d",
			len(one.Attributes), len(nested.NestedObject.Attributes), len(serverAttrTypes))
	}
	for name, typ := range serverAttrTypes {
		for _, attrs := range []map[string]dsschema.Attribute{one.Attributes, nested.NestedObject.Attributes} {
			a, ok := attrs[name]
			if !ok {
				t.Errorf("attribute %q missing", name)
				continue
			}
			if !a.GetType().Equal(typ) {
				t.Errorf("attribute %q is %s, serverAttrTypes says %s", name, a.GetType(), typ)
			}
		}
	}
}

func TestNoDataSourceExposesChannelConfiguration(t *testing.T) {
	for name, ctor := range allDataSources() {
		walkAttributes("", dataSourceSchema(t, ctor).Attributes, func(path string, _ dsschema.Attribute) {
			last := path[strings.LastIndex(path, ".")+1:]
			for _, bad := range []string{"url", "token", "config", "secret", "webhook"} {
				if strings.Contains(strings.ToLower(last), bad) {
					t.Errorf("%s exposes %q", name, path)
				}
			}
		})
	}
}
