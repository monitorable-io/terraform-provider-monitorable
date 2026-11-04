// Package provider implements the Monitorable Terraform provider.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
)

const (
	defaultEndpoint = "https://api.monitorable.io"
	envAPIKey       = "MONITORABLE_API_KEY"
	envEndpoint     = "MONITORABLE_ENDPOINT"
)

var _ provider.Provider = (*monitorableProvider)(nil)

type monitorableProvider struct {
	version string
}

type providerModel struct {
	APIKey   types.String `tfsdk:"api_key"`
	Endpoint types.String `tfsdk:"endpoint"`
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

func (p *monitorableProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage Monitorable servers, their alert template and maintenance mutes through the Monitorable REST API.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				Description: "A Monitorable API key (created in the dashboard under Settings, API keys). " +
					"Can also be set with the MONITORABLE_API_KEY environment variable; the attribute wins when both are set.",
				Optional:  true,
				Sensitive: true,
			},
			"endpoint": schema.StringAttribute{
				Description: "The API origin, scheme and host without a path. Defaults to " + defaultEndpoint + ". " +
					"Can also be set with the MONITORABLE_ENDPOINT environment variable; the attribute wins when both are set. " +
					"An http:// origin whose host is not loopback and does not end in .lan or .local draws a warning, " +
					"because the API key would be sent unencrypted.",
				Optional: true,
			},
		},
	}
}

// Configure builds the API client, checks the key with GET /v1/me and hands the
// client to every resource and data source.
func (p *monitorableProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.APIKey.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Unknown Monitorable API key",
			"The api_key value is not known until apply, so the provider cannot be configured. "+
				"Set it from a value that is known at plan time, or use the "+envAPIKey+" environment variable.")
	}
	if cfg.Endpoint.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Unknown Monitorable endpoint",
			"The endpoint value is not known until apply, so the provider cannot be configured. "+
				"Set it from a value that is known at plan time, or use the "+envEndpoint+" environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	apiKey := firstNonEmpty(strings.TrimSpace(cfg.APIKey.ValueString()), strings.TrimSpace(os.Getenv(envAPIKey)))
	endpoint := firstNonEmpty(cfg.Endpoint.ValueString(), os.Getenv(envEndpoint), defaultEndpoint)
	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing Monitorable API key",
			"api_key is not set: set it in the provider block or in "+envAPIKey)
		return
	}

	c, err := client.New(endpoint, apiKey, "terraform-provider-monitorable/"+p.version+" (+https://monitorable.io/docs/api/terraform/)")
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Invalid Monitorable provider configuration", err.Error())
		return
	}
	// Not an error: development stacks serve plain http on local names.
	if host, plaintext := c.PlaintextHost(); plaintext {
		resp.Diagnostics.AddWarning("Unencrypted Monitorable endpoint",
			fmt.Sprintf("the API key is sent unencrypted to %s; use https", host))
	}

	me, err := c.Me(ctx)
	if err != nil {
		var ae *client.APIError
		switch {
		case errors.As(err, &ae) && ae.Status == http.StatusUnauthorized:
			resp.Diagnostics.AddError("Invalid Monitorable API key", ae.Error())
		case errors.As(err, &ae) && ae.Status == http.StatusForbidden && ae.Code == "api_access_not_in_plan":
			resp.Diagnostics.AddError("API access is not in your plan", ae.Error())
		case errors.As(err, &ae):
			resp.Diagnostics.AddError("The Monitorable API refused the request", ae.Error())
		case isTransportError(err):
			resp.Diagnostics.AddError("Cannot reach the Monitorable API at "+endpoint, err.Error())
		default:
			resp.Diagnostics.AddError("Monitorable API request failed", err.Error())
		}
		return
	}

	tflog.Info(ctx, "configured", map[string]any{
		"organization_id": me.OrganizationID,
		"key_name":        me.KeyName,
		"role":            me.Role,
		"scopes":          me.Scopes,
	})
	resp.DataSourceData = c
	resp.ResourceData = c
}

// isTransportError reports whether err is a network-level failure (the request
// never got an answer), as opposed to a bad response or a cancelled context.
func isTransportError(err error) bool {
	var ue *url.Error
	var ne net.Error
	return errors.As(err, &ue) || errors.As(err, &ne)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (p *monitorableProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewServerResource,
		NewServerAlertTemplateResource,
		NewServerMuteResource,
	}
}

func (p *monitorableProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewServerDataSource,
		NewServersDataSource,
		NewAlertTemplateDataSource,
		NewAlertTemplatesDataSource,
		NewNotificationChannelsDataSource,
	}
}
