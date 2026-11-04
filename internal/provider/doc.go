package provider

// Generate the provider documentation under docs/ from the schemas and examples/.
// The Terraform version is pinned so the output is reproducible; keep it equal to
// TF_VERSION in the Makefile and in .github/workflows/validate.yml.
//go:generate go tool tfplugindocs generate --provider-dir ../.. --provider-name monitorable --tf-version 1.17.1
