.PHONY: build test testacc docs lint

build:
	go build ./...

test:
	go test ./...

testacc:
	TF_ACC=1 go test ./internal/provider/ -v -timeout 30m

docs:
	go generate ./...

lint:
	@test -z "$$(gofmt -l . | tee /dev/stderr)"
	go vet ./...
