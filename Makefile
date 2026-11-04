.PHONY: build test testacc docs lint

# The one Terraform release used to generate docs and run acceptance tests.
# internal/provider/doc.go and .github/workflows/validate.yml repeat this value.
TF_VERSION ?= 1.17.1

build:
	go build ./...

test:
	go test ./...

testacc:
	TF_ACC=1 TF_ACC_TERRAFORM_VERSION=$(TF_VERSION) go test ./internal/provider/ -v -timeout 30m

docs:
	go generate ./...

lint:
	@test -z "$$(gofmt -l . | tee /dev/stderr)"
	go vet ./...
