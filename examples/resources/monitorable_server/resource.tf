resource "monitorable_server" "web" {
  name = "web-01"
}

# Install the agent on the host with the command the API returned when the
# server was created. install_command embeds the agent's API key; it is
# sensitive, so Terraform hides the provisioner's output.
resource "null_resource" "web_agent" {
  depends_on = [monitorable_server.web]

  triggers = {
    server_id = monitorable_server.web.id
  }

  connection {
    type = "ssh"
    host = "203.0.113.10"
    user = "ubuntu"
  }

  provisioner "remote-exec" {
    inline = [monitorable_server.web.install_command]
  }
}
