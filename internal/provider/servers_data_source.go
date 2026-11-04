package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
)

var (
	_ datasource.DataSource              = (*serversDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*serversDataSource)(nil)
)

// NewServersDataSource is the monitorable_servers data source.
func NewServersDataSource() datasource.DataSource { return &serversDataSource{} }

type serversDataSource struct {
	c *client.Client
}

type serversModel struct {
	Status       types.String    `tfsdk:"status"`
	NameContains types.String    `tfsdk:"name_contains"`
	Servers      []serverDSModel `tfsdk:"servers"`
}

func (d *serversDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_servers"
}

func (d *serversDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	const desc = "Lists the servers of the organization, optionally filtered by liveness status or a name fragment. " +
		"Use it to find a server that exists but is not in Terraform state, for example after an interrupted apply, " +
		"and adopt it with `terraform import`."
	const statusDesc = "Only servers with this liveness status: `online`, `stale`, `offline` or `dormant`. Omit for all statuses."
	const containsDesc = "Only servers whose name contains this text; the match is case-insensitive. Omit for all names."
	const serversDesc = "The matching servers, ordered by id. Empty when none match."
	resp.Schema = schema.Schema{
		Description:         desc,
		MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"status": schema.StringAttribute{
				Optional: true, Description: statusDesc, MarkdownDescription: statusDesc,
				Validators: []validator.String{stringvalidator.OneOf("online", "stale", "offline", "dormant")},
			},
			"name_contains": schema.StringAttribute{
				Optional: true, Description: containsDesc, MarkdownDescription: containsDesc,
			},
			"servers": schema.ListNestedAttribute{
				Computed: true, Description: serversDesc, MarkdownDescription: serversDesc,
				NestedObject: schema.NestedAttributeObject{Attributes: serverComputedAttributes()},
			},
		},
	}
}

func (d *serversDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.c = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *serversDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state serversModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.c.ListServers(ctx, client.ListServersParams{
		Status: state.Status.ValueString(),
		Query:  state.NameContains.ValueString(),
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Listing servers failed", err)
		return
	}
	state.Servers = make([]serverDSModel, 0, len(all))
	for i := range all {
		state.Servers = append(state.Servers, serverToModel(&all[i]))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
