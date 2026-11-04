package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
)

const serverAlertTemplateDescription = "Assigns an alert template to a server. A server with no assignment follows the " +
	"organization's default template. Destroying the resource returns the server to the default template; " +
	"it never deletes the template. If someone resets the assignment in the dashboard, the next plan assigns it again."

var (
	_ resource.Resource                = (*serverAlertTemplateResource)(nil)
	_ resource.ResourceWithConfigure   = (*serverAlertTemplateResource)(nil)
	_ resource.ResourceWithImportState = (*serverAlertTemplateResource)(nil)
)

// NewServerAlertTemplateResource is the monitorable_server_alert_template resource.
func NewServerAlertTemplateResource() resource.Resource { return &serverAlertTemplateResource{} }

type serverAlertTemplateResource struct {
	c *client.Client
}

type serverAlertTemplateModel struct {
	ID         types.String `tfsdk:"id"`
	ServerID   types.String `tfsdk:"server_id"`
	TemplateID types.String `tfsdk:"template_id"`
}

func (r *serverAlertTemplateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server_alert_template"
}

func (r *serverAlertTemplateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description:         serverAlertTemplateDescription,
		MarkdownDescription: serverAlertTemplateDescription,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The server id; a server has at most one assignment.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"server_id": schema.StringAttribute{
				Description: "The id of the server, a decimal integer. Changing it assigns the template to the other server.",
				Required:    true,
				Validators:  []validator.String{decimalIDValidator{}},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"template_id": schema.StringAttribute{
				Description: "The id of the alert template, a decimal integer. Changing it reassigns the server in place. " +
					"Use `data \"monitorable_alert_template\"` to look an id up by name. To return a server to the " +
					"organization's default template, destroy this resource.",
				Required:   true,
				Validators: []validator.String{templateIDValidator{}},
			},
		},
	}
}

func (r *serverAlertTemplateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.c = c
}

func (r *serverAlertTemplateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serverAlertTemplateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	serverID, ok := parseID(path.Root("server_id"), plan.ServerID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	templateID, ok := parseID(path.Root("template_id"), plan.TemplateID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	if err := r.c.SetServerAlertTemplate(ctx, serverID, templateID); err != nil {
		addAPIError(&resp.Diagnostics, "Assigning the alert template failed", err)
		return
	}
	plan.ID = types.StringValue(idString(serverID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serverAlertTemplateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serverAlertTemplateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, ok := parseID(path.Root("id"), state.ID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	s, err := r.c.GetServer(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Reading the alert template assignment failed", err)
		return
	}
	if s.AlertTemplateID == 0 {
		// Reset to the default outside Terraform: drop it so the next apply
		// assigns the template again.
		resp.State.RemoveResource(ctx)
		return
	}
	state.ServerID = types.StringValue(idString(s.ID))
	state.TemplateID = types.StringValue(idString(s.AlertTemplateID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serverAlertTemplateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serverAlertTemplateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	serverID, ok := parseID(path.Root("id"), state.ID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	templateID, ok := parseID(path.Root("template_id"), plan.TemplateID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	if err := r.c.SetServerAlertTemplate(ctx, serverID, templateID); err != nil {
		addAPIError(&resp.Diagnostics, "Reassigning the alert template failed", err)
		return
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serverAlertTemplateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serverAlertTemplateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, ok := parseID(path.Root("id"), state.ID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	// Template 0 is "follow the default". A server that is already gone has
	// nothing to reset.
	if err := r.c.SetServerAlertTemplate(ctx, id, 0); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Returning the server to the default alert template failed", err)
	}
}

// ImportState adopts the assignment on a server by the server's decimal id. A
// server that follows the default template has no assignment: Read would drop
// it again and Terraform would report only a generic "non-existent remote
// object", so it is refused here with the reason.
func (r *serverAlertTemplateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, ok := parseID(path.Root("id"), req.ID, &resp.Diagnostics)
	if !ok {
		return
	}
	s, err := r.c.GetServer(ctx, id)
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("Importing the alert template assignment failed", fmt.Sprintf("server %d not found", id))
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Importing the alert template assignment failed", err)
		return
	}
	if s.AlertTemplateID == 0 {
		resp.Diagnostics.AddError("Nothing to import",
			fmt.Sprintf("server %d follows the organization's default template; there is no assignment to import", id))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), types.StringValue(idString(id)))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("server_id"), types.StringValue(idString(id)))...)
	// template_id is filled in by the Read that follows.
}

// decimalIDValidator accepts the canonical decimal form of an API id and
// refuses everything else at plan time, with parseID's message. Unknown values
// (a reference to a resource not yet created) pass.
type decimalIDValidator struct{}

const decimalIDDescription = "must be the decimal integer id of an existing object"

func (decimalIDValidator) Description(context.Context) string { return decimalIDDescription }

func (v decimalIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (decimalIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	parseID(req.Path, req.ConfigValue.ValueString(), &resp.Diagnostics)
}

// templateIDValidator is decimalIDValidator for template_id, with a pointer to
// destroy for "0": the API's 0 means "follow the default", which the provider
// models as the absence of the resource.
type templateIDValidator struct{ decimalIDValidator }

func (templateIDValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !req.ConfigValue.IsNull() && !req.ConfigValue.IsUnknown() && req.ConfigValue.ValueString() == "0" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid template_id",
			"template_id 0 is the organization's default template; destroy the resource to return the server to the organization's default template")
		return
	}
	decimalIDValidator{}.ValidateString(ctx, req, resp)
}
