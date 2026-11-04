// Package provider implements the Monitorable Terraform provider.
package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var _ provider.Provider = (*monitorableProvider)(nil)

type monitorableProvider struct {
	version string
}

// New returns a constructor for the provider, stamped with the release version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &monitorableProvider{version: version}
	}
}

func (p *monitorableProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "monitorable"
	resp.Version = p.version
}

func (p *monitorableProvider) Schema(_ context.Context, _ provider.SchemaRequest, _ *provider.SchemaResponse) {
}

func (p *monitorableProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
}

func (p *monitorableProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

func (p *monitorableProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
