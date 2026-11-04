resource "monitorable_server" "web" {
  name = "web-01"
}

# A maintenance window: alerts are muted until the instant passes. After that
# the resource stays in state as completed (active = false) and is not applied
# again; change `until` to open a new window.
resource "monitorable_server_mute" "window" {
  server_id = monitorable_server.web.id
  until     = "2026-12-01T06:00:00Z"
  reason    = "kernel upgrade"
}

# Mute for the duration of one apply, for example from a deploy pipeline.
# timestamp() changes on every run, so ignore_changes keeps the mute from being
# replaced by the next plan: the window is opened once, when the resource is
# created, and `terraform destroy` lifts it early. A pipeline that wants one
# window per deploy should drop `ignore_changes`: each apply's timestamp() then
# differs from the state's, which replaces the mute and opens a fresh window.
resource "monitorable_server_mute" "deploy" {
  server_id = monitorable_server.web.id
  until     = timeadd(timestamp(), "30m")
  reason    = "deploy"

  lifecycle {
    ignore_changes = [until]
  }
}

# With no `until` the mute lasts until the resource is destroyed.
resource "monitorable_server_mute" "until_destroyed" {
  server_id = monitorable_server.web.id
  reason    = "decommissioning"
}
