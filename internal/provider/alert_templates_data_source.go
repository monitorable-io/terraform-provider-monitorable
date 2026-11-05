package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
)

var (
	_ datasource.DataSource              = (*alertTemplatesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*alertTemplatesDataSource)(nil)
)

// NewAlertTemplatesDataSource is the monitorable_alert_templates data source.
func NewAlertTemplatesDataSource() datasource.DataSource { return &alertTemplatesDataSource{} }

type alertTemplatesDataSource struct {
	c *client.Client
}

type alertTemplatesModel struct {
	Templates []alertTemplateModel `tfsdk:"templates"`
}

func (d *alertTemplatesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_templates"
}

func (d *alertTemplatesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	const desc = "Lists the organization's alert templates. Templates are managed in the dashboard."
	const listDesc = "The alert templates, in the order the API returns them."
	const idDesc = "The template id, a decimal integer."
	const nameDesc = "The template's name."
	const defDesc = "Whether this is the organization's default template, the one servers follow when none is assigned."
	resp.Schema = schema.Schema{
		Description:         desc,
		MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"templates": schema.ListNestedAttribute{
				Computed: true, Description: listDesc, MarkdownDescription: listDesc,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":         schema.StringAttribute{Computed: true, Description: idDesc, MarkdownDescription: idDesc},
					"name":       schema.StringAttribute{Computed: true, Description: nameDesc, MarkdownDescription: nameDesc},
					"is_default": schema.BoolAttribute{Computed: true, Description: defDesc, MarkdownDescription: defDesc},
				}},
			},
		},
	}
}

func (d *alertTemplatesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.c = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *alertTemplatesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	all, err := d.c.ListAlertTemplates(ctx)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Listing alert templates failed", err)
		return
	}
	state := alertTemplatesModel{Templates: make([]alertTemplateModel, 0, len(all))}
	for _, t := range all {
		state.Templates = append(state.Templates, templateToModel(t))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
