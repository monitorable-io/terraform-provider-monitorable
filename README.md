# terraform-provider-monitorable

Terraform provider for [Monitorable](https://monitorable.io): manage servers, alert template assignment and maintenance mutes over the public REST API, from Terraform 1.5 or later or OpenTofu 1.6 or later.

Status: pre-release.

- [Provider documentation](docs/index.md) and the [guide](https://monitorable.io/docs/api/terraform/)
- Resources: `monitorable_server`, `monitorable_server_alert_template`, `monitorable_server_mute`
- Data sources: `monitorable_server`, `monitorable_servers`, `monitorable_alert_template`, `monitorable_alert_templates`, `monitorable_notification_channels`

## Usage

```hcl
terraform {
  required_providers {
    monitorable = {
      source  = "monitorable-io/monitorable"
      version = "~> 0.1"
    }
  }
}

# The API key comes from the MONITORABLE_API_KEY environment variable.
provider "monitorable" {}

resource "monitorable_server" "web" {
  name = "web-01"
}
```

Create an API key in the Monitorable dashboard with the scopes `servers:read servers:write alerts:read alerts:write`, export it as `MONITORABLE_API_KEY`, then run `terraform init` and `terraform apply`. Run `monitorable_server.web.install_command` on the host to install the agent. It is a sensitive value: use it through a provisioner (see the `monitorable_server` example in the docs) or read it with `terraform output -raw`.

The agent's API key is stored in Terraform state as a sensitive value. Treat state as a secret. See the [provider documentation](docs/index.md) for authentication, import, interrupted applies and limits.

## Development

Requires the Go version in `go.mod`.

```sh
make build     # go build ./...
make test      # unit tests
make testacc   # acceptance tests (a few minutes)
make docs      # regenerate docs/ from the schemas, templates/ and examples/
make lint      # gofmt and go vet
```

`docs/` is generated; edit the schema descriptions, `templates/` or `examples/`, run `make docs` and commit the result. CI fails when `docs/` is out of date.

### Acceptance tests

By default `make testacc` runs against an in-process fake of the API that lives in this repository (`internal/client/fake`), so it needs no account and no network.

To run against a real endpoint, set both `MONITORABLE_API_KEY` and `MONITORABLE_ENDPOINT`:

```sh
MONITORABLE_API_KEY=... MONITORABLE_ENDPOINT=https://api.example.com make testacc
```

The tests create servers named `tfacc-*` and delete them when they finish. Use an account you can afford to write to, never a production organization.

## Releasing

Releases are cut by the maintainer. Tag only a commit whose `validate` run on `main` is green; the release builds exactly the tagged tree.

1. Move the `CHANGELOG.md` entries under the new version heading and merge.
2. Push a `vX.Y.Z` tag. The `release.yml` workflow starts and waits for the maintainer's approval in the `production` environment.
3. After approval, GoReleaser builds the archives for every supported platform, signs the `SHA256SUMS` file with GPG and publishes the GitHub release.
4. The Terraform Registry detects the release and lists the new version.

Before the first release, the public half of the signing key must be registered with the Terraform Registry, and the `GPG_PRIVATE_KEY` and `PASSPHRASE` secrets must exist in the `production` environment. The signing secrets are available to the release job only.

## Security

See [SECURITY.md](SECURITY.md) to report a vulnerability.

## License

Apache-2.0. See [LICENSE](LICENSE).
