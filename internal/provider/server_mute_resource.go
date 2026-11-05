package provider

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
)

const serverMuteDescription = "A maintenance mute on a server's alerts. Set `until` for a window; omit it to mute until the " +
	"resource is destroyed. A mute whose `until` has passed stays in state as completed and is not re-applied; " +
	"change `until` to open a new window. Destroying the resource unmutes the server."

// untilTolerance is how far the API's until may sit from the configured one
// and still be the same instant: Postgres timestamptz keeps microseconds and
// rounds to the nearest, so a nanosecond-precision value reads back changed
// by up to half a microsecond.
const untilTolerance = time.Microsecond

var (
	_ resource.Resource                = (*serverMuteResource)(nil)
	_ resource.ResourceWithConfigure   = (*serverMuteResource)(nil)
	_ resource.ResourceWithImportState = (*serverMuteResource)(nil)
)

// NewServerMuteResource is the monitorable_server_mute resource.
func NewServerMuteResource() resource.Resource { return &serverMuteResource{} }

type serverMuteResource struct {
	c *client.Client
}

type serverMuteModel struct {
	ID       types.String      `tfsdk:"id"`
	ServerID types.String      `tfsdk:"server_id"`
	Until    timetypes.RFC3339 `tfsdk:"until"`
	Reason   types.String      `tfsdk:"reason"`
	Active   types.Bool        `tfsdk:"active"`
}

func (r *serverMuteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server_mute"
}

func (r *serverMuteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description:         serverMuteDescription,
		MarkdownDescription: serverMuteDescription,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The server id; a server has at most one mute.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"server_id": schema.StringAttribute{
				Description:   "The id of the server to mute, a decimal integer. Changing it replaces the resource.",
				Required:      true,
				Validators:    []validator.String{decimalIDValidator{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"until": schema.StringAttribute{
				Description: "When the mute ends (RFC 3339; any offset, compared as an instant). Omit it to mute until " +
					"the resource is destroyed. It must be in the future when the mute is created, and at most 366 days " +
					"ahead. Changing it to another instant replaces the resource, which opens a new window. A window lifted " +
					"early in the dashboard is re-created on the next apply only while `until` is still in the future; " +
					"once it has passed, the resource is a completed record and is left alone.",
				CustomType:    timetypes.RFC3339Type{},
				Optional:      true,
				PlanModifiers: []planmodifier.String{requiresReplaceIfInstantChanges{}},
			},
			"reason": schema.StringAttribute{
				Description: "Why the server is muted, at most 200 characters, shown in the dashboard. It must not start " +
					"or end with whitespace or contain control characters. Changing it replaces the resource.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.UTF8LengthAtMost(200),
					cleanReasonValidator{},
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"active": schema.BoolAttribute{
				Description: "Whether the server is muted now, as the API reports it. False once a window has passed " +
					"(the resource then stays in state as completed).",
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *serverMuteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.c = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *serverMuteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serverMuteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, ok := parseID(path.Root("server_id"), plan.ServerID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	until, diags := timeFrom(plan.Until)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	out, err := r.c.MuteServerAlerts(ctx, id, until, plan.Reason.ValueString())
	if err != nil {
		addAPIError(&resp.Diagnostics, "Muting the server's alerts failed", err)
		return
	}
	// until and reason stay as configured (the API trims and rounds them; the
	// validators and the Read tolerance keep that from showing as a diff).
	plan.ID = types.StringValue(idString(id))
	plan.Active = types.BoolValue(out.Muted)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serverMuteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serverMuteModel
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
		addAPIError(&resp.Diagnostics, "Reading the server's mute failed", err)
		return
	}
	stateUntil, diags := timeFrom(state.Until)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	switch muteReadAction(stateUntil, s.AlertsMuted, time.Now()) {
	case readInert:
		// R21: the window has passed (or the API says it has, within the
		// clock-skew grace), so the resource is a completed record. until and
		// reason stay as they are, whatever the API shows now (a mute someone
		// set afterwards is theirs); active mirrors the API.
		state.Active = types.BoolValue(s.AlertsMuted)
	case readAdopt:
		state.Active = types.BoolValue(true)
		state.Until = readUntil(stateUntil, state.Until, s.AlertsMutedUntil)
		if s.AlertsMutedReason == "" && state.Reason.IsNull() {
			state.Reason = types.StringNull()
		} else {
			state.Reason = types.StringValue(s.AlertsMutedReason)
		}
	default:
		// Unmuted early, or an indefinite mute lifted in the dashboard: drop
		// it so the next apply mutes again.
		resp.State.RemoveResource(ctx)
		return
	}
	state.ServerID = types.StringValue(idString(s.ID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// muteGrace is the clock-skew allowance for a window that is about to end: if
// the API already reports the server unmuted and until is no more than this far
// ahead of our clock, the window is treated as completed, not lifted early.
const muteGrace = time.Minute

type muteRead int

const (
	readRemove muteRead = iota // not ours any more: drop it, the next apply mutes again
	readAdopt                  // muted, window open (or indefinite): take the API's until and reason
	readInert                  // window completed: keep until and reason, active mirrors the API
)

// muteReadAction chooses Read's branch. stateUntil is the until in state (nil
// for an indefinite mute), apiMuted is the API's alerts_muted.
//
//   - until in the past: inert, whatever the API shows (R21).
//   - muted otherwise: adopt the API's values.
//   - unmuted with until within muteGrace of now or earlier: inert (the
//     window completed; Review Focus 2).
//   - unmuted otherwise (indefinite, or a window lifted early): remove.
func muteReadAction(stateUntil *time.Time, apiMuted bool, now time.Time) muteRead {
	switch {
	case stateUntil != nil && !stateUntil.After(now):
		return readInert
	case apiMuted:
		return readAdopt
	case stateUntil != nil && !stateUntil.After(now.Add(muteGrace)):
		return readInert
	}
	return readRemove
}

// readUntil is the until for a muted server: the state's value when the API's
// is the same instant (to the microsecond), otherwise the API's.
func readUntil(stateUntil *time.Time, stateValue timetypes.RFC3339, apiUntil *time.Time) timetypes.RFC3339 {
	if stateUntil != nil && apiUntil != nil && apiUntil.Sub(*stateUntil).Abs() <= untilTolerance {
		return stateValue
	}
	return rfc3339(apiUntil)
}

// Update runs only when until is respelled as the same instant (every other
// change replaces the resource): nothing changes in the API.
func (r *serverMuteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state serverMuteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	plan.Active = state.Active
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serverMuteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serverMuteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, ok := parseID(path.Root("id"), state.ID.ValueString(), &resp.Diagnostics)
	if !ok {
		return
	}
	// R21: after the window has passed the mute is no longer ours; a mute on
	// the server now was set by someone else and is left alone.
	until, diags := timeFrom(state.Until)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if until != nil && !until.After(time.Now()) {
		return
	}
	// Unmuting a server that is not muted is success, and so is one that is
	// already gone (the client maps 404 to nil).
	if err := r.c.UnmuteServerAlerts(ctx, id); err != nil {
		addAPIError(&resp.Diagnostics, "Unmuting the server's alerts failed", err)
	}
}

// ImportState adopts the mute on a server by the server's decimal id. A server
// that is not muted has no mute to import: Read would drop it again and
// Terraform would report only a generic "non-existent remote object", so it is
// refused here with the reason.
func (r *serverMuteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, ok := parseID(path.Root("id"), req.ID, &resp.Diagnostics)
	if !ok {
		return
	}
	s, err := r.c.GetServer(ctx, id)
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("Importing the mute failed", fmt.Sprintf("server %d not found", id))
		return
	}
	if err != nil {
		addAPIError(&resp.Diagnostics, "Importing the mute failed", err)
		return
	}
	if !s.AlertsMuted {
		resp.Diagnostics.AddError("Nothing to import", fmt.Sprintf("server %d is not muted; there is no mute to import", id))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), types.StringValue(idString(id)))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("server_id"), types.StringValue(idString(id)))...)
	// until, reason and active are filled in by the Read that follows.
}

// requiresReplaceIfInstantChanges replaces the resource when until moves to
// another instant, or appears or disappears. The same instant spelled
// differently (+01:00 for Z) is not a change of the window.
type requiresReplaceIfInstantChanges struct{}

func (requiresReplaceIfInstantChanges) Description(context.Context) string {
	return "Replaces the resource when until is set to another instant."
}

func (m requiresReplaceIfInstantChanges) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (requiresReplaceIfInstantChanges) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return // create or destroy
	}
	if req.PlanValue.Equal(req.StateValue) {
		return
	}
	if !req.PlanValue.IsNull() && !req.PlanValue.IsUnknown() && !req.StateValue.IsNull() && !req.StateValue.IsUnknown() {
		p, perr := time.Parse(time.RFC3339Nano, req.PlanValue.ValueString())
		s, serr := time.Parse(time.RFC3339Nano, req.StateValue.ValueString())
		if perr == nil && serr == nil && p.Sub(s).Abs() <= untilTolerance {
			return
		}
	}
	resp.RequiresReplace = true
}

// cleanReasonValidator refuses a reason the API would change or refuse: it
// trims surrounding whitespace before it stores the reason (so the value would
// come back different and diff on every plan) and rejects control characters.
type cleanReasonValidator struct{}

const cleanReasonMessage = "reason must not start or end with whitespace or contain control characters"

func (cleanReasonValidator) Description(context.Context) string { return cleanReasonMessage }

func (v cleanReasonValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (cleanReasonValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	if s != strings.TrimSpace(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid mute reason", fmt.Sprintf("%s, got %q", cleanReasonMessage, s))
	}
}
