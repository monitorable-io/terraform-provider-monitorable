package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
)

const serverDescription = "A server registered in Monitorable. Creating it returns the agent's API key " +
	"and install command once; both are stored in state as sensitive values, so " +
	"treat the state as a secret. Destroying it deletes the server and its " +
	"metrics history. An imported server has no `agent_api_key`; rotate it in the " +
	"dashboard or with `POST /v1/servers/{id}/agent-key:rotate` if the key is " +
	"lost."

var (
	_ resource.Resource                = (*serverResource)(nil)
	_ resource.ResourceWithConfigure   = (*serverResource)(nil)
	_ resource.ResourceWithImportState = (*serverResource)(nil)
)

// NewServerResource is the monitorable_server resource.
func NewServerResource() resource.Resource { return &serverResource{} }

type serverResource struct {
	c *client.Client
}

// serverModel is the monitorable_server state. AgentAPIKey and InstallCommand
// are known only from the create response; they are never re-read, and they
// must never be logged.
type serverModel struct {
	ID             types.String      `tfsdk:"id"`
	Name           types.String      `tfsdk:"name"`
	AgentAPIKey    types.String      `tfsdk:"agent_api_key"`
	InstallCommand types.String      `tfsdk:"install_command"`
	CreatedAt      timetypes.RFC3339 `tfsdk:"created_at"`
	CreatedVia     types.String      `tfsdk:"created_via"`
}

func (r *serverResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server"
}

func (r *serverResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description:         serverDescription,
		MarkdownDescription: serverDescription,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The server id, a decimal integer.",
				Computed:      true,
				PlanModifiers: keep,
			},
			"name": schema.StringAttribute{
				Description: "The display name, 1 to 100 characters, without leading or trailing whitespace. " +
					"Changing it renames the server in place.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.UTF8LengthBetween(1, 100),
					trimmedNameValidator{},
				},
			},
			"agent_api_key": schema.StringAttribute{
				Description: "The agent's API key, returned once when the server is created. " +
					"Null for an imported server, or when the create response was lost and replayed.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
			"install_command": schema.StringAttribute{
				Description: "The command that installs the agent on the host, exactly as the API returned it. " +
					"It embeds `agent_api_key`. Null whenever `agent_api_key` is.",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: keep,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the server was created (RFC 3339).",
				CustomType:    timetypes.RFC3339Type{},
				Computed:      true,
				PlanModifiers: keep,
			},
			"created_via": schema.StringAttribute{
				Description:   "How the server was created, as the API reports it: `api` for a server this provider created.",
				Computed:      true,
				PlanModifiers: keep,
			},
		},
	}
}

func (r *serverResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *serverResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serverModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// One key per Create; the client resends it on every retry, so a lost
	// response is answered by a replay instead of a second server.
	out, err := r.c.CreateServer(ctx, plan.Name.ValueString(), uuid.NewString())
	if err != nil {
		detail := apiErrorDetail(err)
		if createMayHaveSucceeded(err) {
			detail += "\n\n" + orphanHint
		}
		resp.Diagnostics.AddError("Creating the server failed", detail)
		return
	}

	state := serverModel{
		AgentAPIKey:    nullIfEmpty(out.AgentAPIKey),
		InstallCommand: nullIfEmpty(out.InstallCommand),
	}
	setServer(&state, &out.Server)
	if out.AgentAPIKey == "" && out.Recovery != "" {
		id := idString(out.Server.ID)
		resp.Diagnostics.AddWarning("The server's agent key was lost",
			"server "+id+" was created but its agent key was lost in a retried request; "+
				"rotate it in the dashboard or with POST /v1/servers/"+id+"/agent-key:rotate")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serverResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serverModel
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
		// Deleted outside Terraform: drop it so the plan re-creates it.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Reading the server failed", err)
		return
	}
	// agent_api_key and install_command stay as they are in state.
	setServer(&state, s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serverResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serverModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, ok := parseID(path.Root("id"), state.ID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}

	s, err := r.c.UpdateServer(ctx, id, plan.Name.ValueString())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Renaming the server failed", err)
		return
	}
	setServer(&state, s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serverResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, ok := parseID(path.Root("id"), state.ID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	// A server that is already gone is success (the client maps 404 to nil).
	if err := r.c.DeleteServer(ctx, id); err != nil {
		addAPIError(&resp.Diagnostics, "Deleting the server failed", err)
	}
}

// ImportState adopts a server by its decimal id. The agent key cannot be
// recovered, so agent_api_key and install_command stay null.
func (r *serverResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, ok := parseID(path.Root("id"), req.ID, &resp.Diagnostics); !ok {
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// setServer copies the API's view of a server into m; the key and install
// command are left alone.
func setServer(m *serverModel, s *client.Server) {
	m.ID = types.StringValue(idString(s.ID))
	m.Name = types.StringValue(s.Name)
	m.CreatedAt = rfc3339(s.CreatedAt)
	m.CreatedVia = types.StringValue(s.CreatedVia)
}

func nullIfEmpty(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// trimmedNameValidator refuses names the API would change on the way in: the
// backend strips NUL and trims surrounding whitespace before it stores a name,
// so such a name would come back different and diff on every plan.
type trimmedNameValidator struct{}

const trimmedNameMessage = "name must not start or end with whitespace or contain NUL"

func (trimmedNameValidator) Description(context.Context) string { return trimmedNameMessage }

func (v trimmedNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (trimmedNameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	if s != strings.TrimSpace(s) || strings.ContainsRune(s, 0) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid server name", fmt.Sprintf("%s, got %q", trimmedNameMessage, s))
	}
}

// orphanHint follows a failed create that may still have created the server.
const orphanHint = "The server may have been created before the request failed. " +
	"Check `data \"monitorable_servers\"` or the dashboard and `terraform import` it " +
	"before applying again, or a second server will be created."

// createMayHaveSucceeded reports whether a failed POST /v1/servers can have
// created the server anyway: a transport error (an attempt may have reached
// the API before the retries ran out), a cancelled or expired context, an
// unreadable 2xx, or a 5xx (a 500 is not retried). A 4xx is a refusal: nothing
// was created.
func createMayHaveSucceeded(err error) bool {
	var ae *client.APIError
	return !errors.As(err, &ae) || ae.Status >= 500
}

// addAPIError reports a failed API call: summary names the operation and the
// detail is apiErrorDetail(err).
func addAPIError(diags *diag.Diagnostics, summary string, err error) {
	diags.AddError(summary, apiErrorDetail(err))
}

// apiErrorDetail is the error text (an *APIError renders title, code, detail
// and request id; the client has already redacted the bearer). A plan-limit
// refusal says how to get past it.
func apiErrorDetail(err error) string {
	detail := err.Error()
	if client.IsPlanLimit(err) {
		detail += "\n\nRaise the plan limit at https://app.monitorable.io/billing or remove a server."
	}
	return detail
}
