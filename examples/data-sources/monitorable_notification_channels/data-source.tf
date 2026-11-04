# Channels are managed in the dashboard; their settings are never exposed.
data "monitorable_notification_channels" "all" {}

output "enabled_channel_names" {
  value = [for c in data.monitorable_notification_channels.all.channels : c.name if c.enabled]
}
