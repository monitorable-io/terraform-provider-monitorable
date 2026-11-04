# The name is exact and case-sensitive.
data "monitorable_alert_template" "strict" {
  name = "Strict"
}

resource "monitorable_server" "db" {
  name = "db-01"
}

resource "monitorable_server_alert_template" "db" {
  server_id   = monitorable_server.db.id
  template_id = data.monitorable_alert_template.strict.id
}
