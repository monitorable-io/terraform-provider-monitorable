# By exact, case-sensitive name. It is an error if no server, or more than
# one, has that name; look the server up by id then.
data "monitorable_server" "web" {
  name = "web-01"
}

# By id.
data "monitorable_server" "by_id" {
  id = "42"
}

output "web_status" {
  value = data.monitorable_server.web.status
}
