terraform {
  required_providers {
    monitorable = {
      source  = "monitorable-io/monitorable"
      version = "~> 0.1"
    }
  }
}

# The API key is read from the MONITORABLE_API_KEY environment variable, so no
# credential appears in the configuration. Set `api_key` only from a secret
# store or a sensitive variable, never as a literal.
provider "monitorable" {}
