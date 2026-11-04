resource "monitorable_server" "web" {
  name = "web-01"
}

# Look the template up by name instead of hard-coding its id.
data "monitorable_alert_template" "strict" {
  name = "Strict"
}

# Servers with no assignment follow the organization's default template.
# Destroying this resource returns web-01 to that default; the template stays.
resource "monitorable_server_alert_template" "web" {
  server_id   = monitorable_server.web.id
  template_id = data.monitorable_alert_template.strict.id
}
