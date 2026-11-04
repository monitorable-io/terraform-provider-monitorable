data "monitorable_alert_templates" "all" {}

output "default_template_id" {
  value = one([for t in data.monitorable_alert_templates.all.templates : t.id if t.is_default])
}
