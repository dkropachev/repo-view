include make/grammar.mk
include make/release.mk
include make/tokenbench.mk

.PHONY: ci-vet ci-test ci-build ci-json ci-no-bash ci-lint ci-fieldalignment

ci-vet:
	go vet ./...

ci-test:
	go test ./... -count=1

ci-build:
	go build ./cmd/...

ci-json:
	go run -mod=readonly ./internal/cmd/project-check -mode json

ci-no-bash:
	go run -mod=readonly ./internal/cmd/project-check -mode no-bash

ci-lint:
	golangci-lint run --config=.golangci.yml

ci-fieldalignment:
	golangci-lint run --config=.golangci-fieldalignment.yml
