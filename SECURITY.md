# Security policy

The provider stores the agent API key of each server it creates in Terraform state as a
sensitive value. Treat state as a secret: keep it in an encrypted backend with restricted
access, and never commit it.

## Reporting a vulnerability

Please do **not** open a public issue for a security problem.

- Preferred: [report privately through GitHub](https://github.com/monitorable-io/terraform-provider-monitorable/security/advisories/new)
  (private vulnerability reporting is enabled on this repository).
- Alternatively, email hello@monitorable.io with `security` in the subject.

Include the provider version (`terraform version` lists it), the Terraform or OpenTofu
version, and steps to reproduce. You will get an acknowledgement within a few days and a fix
or a mitigation before any public disclosure.

## Supported versions

Only the latest released version is supported.
