package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timetypes/timetypes"
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
	_ datasource.DataSource                     = (*serverDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*serverDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*serverDataSource)(nil)
)

// NewServerDataSource is the monitorable_server data source.
func NewServerDataSource() datasource.DataSource { return &serverDataSource{} }

type serverDataSource struct {
	c *client.Client
}

// serverDSModel is one server as the data sources show it: the top level of
// monitorable_server and every element of monitorable_servers.servers.
type serverDSModel struct {
	ID               types.String      `tfsdk:"id"`
	Name             types.String      `tfsdk:"name"`
	Status           types.String      `tfsdk:"status"`
	Triage           types.String      `tfsdk:"triage"`
	IP               types.String      `tfsdk:"ip"`
	IPv4             types.String      `tfsdk:"ipv4"`
	IPv6             types.String      `tfsdk:"ipv6"`
	Location         types.String      `tfsdk:"location"`
	CountryCode      types.String      `tfsdk:"country_code"`
	AgentVersion     types.String      `tfsdk:"agent_version"`
	LastSeenAt       timetypes.RFC3339 `tfsdk:"last_seen_at"`
	CreatedAt        timetypes.RFC3339 `tfsdk:"created_at"`
	UptimeSeconds    types.Float64     `tfsdk:"uptime_seconds"`
	AlertsMuted      types.Bool        `tfsdk:"alerts_muted"`
	AlertsMutedUntil timetypes.RFC3339 `tfsdk:"alerts_muted_until"`
	AlertTemplateID  types.String      `tfsdk:"alert_template_id"`
	OpenIncidents    types.Int64       `tfsdk:"open_incidents"`
	UnackedFiring    types.Int64       `tfsdk:"unacked_firing"`
	CreatedVia       types.String      `tfsdk:"created_via"`
}

// serverComputedAttributes is the schema of a server object with every
// attribute computed; monitorable_server overrides id and name to accept
// input. Defined once for both server data sources.
func serverComputedAttributes() map[string]schema.Attribute {
	str := func(desc string) schema.Attribute {
		return schema.StringAttribute{Computed: true, Description: desc, MarkdownDescription: desc}
	}
	ts := func(desc string) schema.Attribute {
		return schema.StringAttribute{Computed: true, CustomType: timetypes.RFC3339Type{}, Description: desc, MarkdownDescription: desc}
	}
	return map[string]schema.Attribute{
		"id":            str("The server id, a decimal integer."),
		"name":          str("The server's display name."),
		"status":        str("Liveness: `online`, `stale`, `offline` or `dormant`. A server that has never reported is `offline` (`dormant` after seven days)."),
		"triage":        str("The dashboard's triage tier for the server, for example `healthy` or `unreachable`. A server that has never reported is `unreachable`."),
		"ip":            str("The address the agent last reported from. Empty until the agent first reports."),
		"ipv4":          str("The IPv4 address the agent last reported from. Empty when there is none."),
		"ipv6":          str("The IPv6 address the agent last reported from. Empty when there is none."),
		"location":      str("The server's location as the API reports it. Empty when unknown."),
		"country_code":  str("The two-letter country code of the server's location. Empty when unknown."),
		"agent_version": str("The version of the agent running on the server. Empty until the agent first reports."),
		"last_seen_at":  ts("When the agent last reported (RFC 3339, UTC). Null if it never has."),
		"created_at":    ts("When the server was created (RFC 3339, UTC)."),
		"uptime_seconds": schema.Float64Attribute{Computed: true,
			Description:         "The host's uptime in seconds as last reported; 0 until the agent first reports.",
			MarkdownDescription: "The host's uptime in seconds as last reported; `0` until the agent first reports."},
		"alerts_muted": schema.BoolAttribute{Computed: true,
			Description:         "Whether alerts for the server are muted right now.",
			MarkdownDescription: "Whether alerts for the server are muted right now."},
		"alerts_muted_until": ts("When the current mute ends (RFC 3339, UTC). Null when alerts are not muted."),
		"alert_template_id": str("The id of the alert template assigned to the server, a decimal integer. " +
			"`\"0\"` means the server follows the organization's default template."),
		"open_incidents": schema.Int64Attribute{Computed: true,
			Description:         "The number of open incidents on the server.",
			MarkdownDescription: "The number of open incidents on the server."},
		"unacked_firing": schema.Int64Attribute{Computed: true,
			Description:         "The number of firing incidents on the server that nobody has acknowledged.",
			MarkdownDescription: "The number of firing incidents on the server that nobody has acknowledged."},
		"created_via": str("How the server was created, as the API reports it: for example `api` for a server created through the API or this provider, `dashboard` for one created in the dashboard."),
	}
}

// serverToModel maps the API's server onto the shared model.
func serverToModel(s *client.Server) serverDSModel {
	return serverDSModel{
		ID:               types.StringValue(idString(s.ID)),
		Name:             types.StringValue(s.Name),
		Status:           types.StringValue(s.Status),
		Triage:           types.StringValue(s.Triage),
		IP:               types.StringValue(s.IP),
		IPv4:             types.StringValue(s.IPv4),
		IPv6:             types.StringValue(s.IPv6),
		Location:         types.StringValue(s.Location),
		CountryCode:      types.StringValue(s.CountryCode),
		AgentVersion:     types.StringValue(s.AgentVersion),
		LastSeenAt:       rfc3339(s.LastSeenAt),
		CreatedAt:        rfc3339(s.CreatedAt),
		UptimeSeconds:    types.Float64Value(s.UptimeSeconds),
		AlertsMuted:      types.BoolValue(s.AlertsMuted),
		AlertsMutedUntil: rfc3339(s.AlertsMutedUntil),
		AlertTemplateID:  types.StringValue(idString(s.AlertTemplateID)),
		OpenIncidents:    types.Int64Value(s.OpenIncidents),
		UnackedFiring:    types.Int64Value(s.UnackedFiring),
		CreatedVia:       types.StringValue(s.CreatedVia),
	}
}

func (d *serverDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server"
}

func (d *serverDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := serverComputedAttributes()
	attrs["id"] = schema.StringAttribute{
		Optional: true, Computed: true,
		Description:         "The server id, a decimal integer. Set exactly one of `id` and `name`.",
		MarkdownDescription: "The server id, a decimal integer. Set exactly one of `id` and `name`.",
		Validators:          []validator.String{decimalIDValidator{}},
	}
	attrs["name"] = schema.StringAttribute{
		Optional: true, Computed: true,
		Description: "The server's exact display name; the match is case-sensitive. It is an error if no server, " +
			"or more than one server, has this name; look the server up by `id` in that case. Set exactly one of `id` and `name`.",
		MarkdownDescription: "The server's exact display name; the match is case-sensitive. It is an error if no server, " +
			"or more than one server, has this name; look the server up by `id` in that case. Set exactly one of `id` and `name`.",
		Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
	}
	const desc = "Looks up one server by `id` or by exact `name` and exposes its state. Fails when the server does not exist."
	resp.Schema = schema.Schema{Description: desc, MarkdownDescription: desc, Attributes: attrs}
}

func (d *serverDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *serverDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.c = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *serverDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg serverDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var srv *client.Server
	if !cfg.ID.IsNull() {
		id, ok := parseID(path.Root("id"), cfg.ID.ValueString(), &resp.Diagnostics)
		if !ok {
			return
		}
		s, err := d.c.GetServer(ctx, id)
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "Server not found", fmt.Sprintf("server %d not found", id))
			return
		}
		if err != nil {
			addAPIError(&resp.Diagnostics, "Reading the server failed", err)
			return
		}
		srv = s
	} else {
		name := cfg.Name.ValueString()
		// ?q= is a case-insensitive substring; the match itself is exact.
		all, err := d.c.ListServers(ctx, client.ListServersParams{Query: name})
		if err != nil {
			addAPIError(&resp.Diagnostics, "Listing servers failed", err)
			return
		}
		var match []client.Server
		for _, s := range all {
			if s.Name == name {
				match = append(match, s)
			}
		}
		switch len(match) {
		case 0:
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Server not found", fmt.Sprintf("no server named %q", name))
			return
		case 1:
			srv = &match[0]
		default:
			ids := make([]int64, len(match))
			for i, s := range match {
				ids[i] = s.ID
			}
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Server name is ambiguous",
				fmt.Sprintf("%d servers are named %q (ids %s); look it up by id", len(match), name, idList(ids)))
			return
		}
	}

	state := serverToModel(srv)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
