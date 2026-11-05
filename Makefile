.PHONY: build test testacc testacc-tofu docs lint

# The one Terraform release used to generate docs and run acceptance tests.
# internal/provider/doc.go and .github/workflows/validate.yml repeat this value.
TF_VERSION ?= 1.17.1

build:
	go build ./...

test:
	go test ./...

testacc:
	TF_ACC=1 TF_ACC_TERRAFORM_VERSION=$(TF_VERSION) go test ./internal/provider/ -v -timeout 30m

# The acceptance suite on OpenTofu. TOFU is the path to a tofu binary; the
# provider host and namespace make the test harness's provider address match
# the one OpenTofu resolves the unqualified "monitorable" provider to.
testacc-tofu:
	@test -x "$(TOFU)" || { echo "set TOFU to the path of a tofu binary" >&2; exit 1; }
	TF_ACC=1 TF_ACC_TERRAFORM_PATH="$(TOFU)" TF_ACC_PROVIDER_HOST=registry.opentofu.org \
		TF_ACC_PROVIDER_NAMESPACE=hashicorp go test ./internal/provider/ -v -timeout 30m

docs:
	go generate ./...

lint:
	@test -z "$$(gofmt -l . | tee /dev/stderr)"
	go vet ./...
