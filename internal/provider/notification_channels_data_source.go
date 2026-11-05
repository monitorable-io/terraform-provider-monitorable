package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/monitorable-io/terraform-provider-monitorable/internal/client"
)

var (
	_ datasource.DataSource              = (*notificationChannelsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*notificationChannelsDataSource)(nil)
)

// NewNotificationChannelsDataSource is the monitorable_notification_channels
// data source. It never exposes a channel's settings (addresses, URLs,
// tokens): the API does not return them.
func NewNotificationChannelsDataSource() datasource.DataSource {
	return &notificationChannelsDataSource{}
}

type notificationChannelsDataSource struct {
	c *client.Client
}

type notificationChannelModel struct {
	ID      types.String `tfsdk:"id"`
	Type    types.String `tfsdk:"type"`
	Name    types.String `tfsdk:"name"`
	Enabled types.Bool   `tfsdk:"enabled"`
}

type notificationChannelsModel struct {
	Channels []notificationChannelModel `tfsdk:"channels"`
}

func (d *notificationChannelsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_notification_channels"
}

func (d *notificationChannelsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	const desc = "Lists the organization's notification channels: id, type, name and whether each is enabled. " +
		"Channels are managed in the dashboard and their settings are never exposed."
	const listDesc = "The notification channels, in the order the API returns them."
	const idDesc = "The channel id, a decimal integer."
	const typeDesc = "The channel type as the API reports it, for example `email` or `slack`."
	const nameDesc = "The channel's name."
	const enabledDesc = "Whether the channel currently receives notifications."
	resp.Schema = schema.Schema{
		Description:         desc,
		MarkdownDescription: desc,
		Attributes: map[string]schema.Attribute{
			"channels": schema.ListNestedAttribute{
				Computed: true, Description: listDesc, MarkdownDescription: listDesc,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":      schema.StringAttribute{Computed: true, Description: idDesc, MarkdownDescription: idDesc},
					"type":    schema.StringAttribute{Computed: true, Description: typeDesc, MarkdownDescription: typeDesc},
					"name":    schema.StringAttribute{Computed: true, Description: nameDesc, MarkdownDescription: nameDesc},
					"enabled": schema.BoolAttribute{Computed: true, Description: enabledDesc, MarkdownDescription: enabledDesc},
				}},
			},
		},
	}
}

func (d *notificationChannelsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.c = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (d *notificationChannelsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	all, err := d.c.ListNotificationChannels(ctx)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Listing notification channels failed", err)
		return
	}
	state := notificationChannelsModel{Channels: make([]notificationChannelModel, 0, len(all))}
	for _, c := range all {
		state.Channels = append(state.Channels, notificationChannelModel{
			ID:      types.StringValue(idString(c.ID)),
			Type:    types.StringValue(c.Type),
			Name:    types.StringValue(c.Name),
			Enabled: types.BoolValue(c.Enabled),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
