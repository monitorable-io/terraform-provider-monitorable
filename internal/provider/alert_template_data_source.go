package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
)

var (
	_ datasource.DataSource                     = (*alertTemplateDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*alertTemplateDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*alertTemplateDataSource)(nil)
)

// NewAlertTemplateDataSource is the monitorable_alert_template data source.
func NewAlertTemplateDataSource() datasource.DataSource { return &alertTemplateDataSource{} }

type alertTemplateDataSource struct {
	c *client.Client
}

// alertTemplateModel is one alert template; it is the top level of
// monitorable_alert_template and each element of monitorable_alert_templates.
type alertTemplateModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	IsDefault types.Bool   `tfsdk:"is_default"`
}

func templateToModel(t client.AlertTemplate) alertTemplateModel {
	return alertTemplateModel{
		ID:        types.StringValue(idString(t.ID)),
		Name:      types.StringValue(t.Name),
		IsDefault: types.BoolValue(t.IsDefault),
	}
}

func (d *alertTemplateDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_template"
}

func (d *alertTemplateDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	const desc = "Looks up one alert template by `id` or by exact `name`. Fails when the template does not exist. " +
		"Templates are managed in the dashboard; use the id with `monitorable_server_alert_template`."
	const idDesc = "The template id, a decimal integer. Set exactly one of `id` and `name`."
	const nameDesc = "The template's exact name; the match is case-sensitive. It is an error if no template, or more than one, " +
		"has this name; look it up by `id` in that case. Set exactly one of `id` and `name`."
	const defDesc = "Whether this is the organization's default template, the one servers follow when none is assigned."
	resp.Schema = schema.Schema{
		Description:         desc,
		MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional: true, Computed: true, Description: idDesc, MarkdownDescription: idDesc,
				Validators: []validator.String{decimalIDValidator{}},
			},
			"name": schema.StringAttribute{
				Optional: true, Computed: true, Description: nameDesc, MarkdownDescription: nameDesc,
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"is_default": schema.BoolAttribute{Computed: true, Description: defDesc, MarkdownDescription: defDesc},
		},
	}
}

func (d *alertTemplateDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *alertTemplateDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.c = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *alertTemplateDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg alertTemplateModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var wantID int64
	if !cfg.ID.IsNull() {
		id, ok := parseID(path.Root("id"), cfg.ID.ValueString(), &resp.Diagnostics)
		if !ok {
			return
		}
		wantID = id
	}
	all, err := d.c.ListAlertTemplates(ctx)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Listing alert templates failed", err)
		return
	}

	var match []client.AlertTemplate
	for _, t := range all {
		if (wantID != 0 && t.ID == wantID) || (wantID == 0 && t.Name == cfg.Name.ValueString()) {
			match = append(match, t)
		}
	}
	switch {
	case len(match) == 0 && wantID != 0:
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Alert template not found", fmt.Sprintf("alert template %d not found", wantID))
		return
	case len(match) == 0:
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Alert template not found", fmt.Sprintf("no alert template named %q", cfg.Name.ValueString()))
		return
	case len(match) > 1:
		ids := make([]int64, len(match))
		for i, t := range match {
			ids[i] = t.ID
		}
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Alert template name is ambiguous",
			fmt.Sprintf("%d alert templates are named %q (ids %s); look it up by id", len(match), cfg.Name.ValueString(), idList(ids)))
		return
	}

	state := templateToModel(match[0])
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
