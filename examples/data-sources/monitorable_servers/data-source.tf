# Every server whose name contains "web" (case-insensitive) and that has not
# reported for a while.
data "monitorable_servers" "quiet_web" {
  name_contains = "web"
  status        = "offline"
}

output "quiet_web_ids" {
  value = [for s in data.monitorable_servers.quiet_web.servers : s.id]
}

# After an interrupted apply, find the server that was created but never made
# it into state, then adopt it with `terraform import monitorable_server.web <id>`.
data "monitorable_servers" "all" {}
