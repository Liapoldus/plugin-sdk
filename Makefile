.PHONY: check test build vet contracts contracts-check lint lint-go lint-ts typecheck

export GOFLAGS ?= -p=1
GO_PACKAGES := ./domain/... ./application/... ./infrastructure/... ./presentation/... ./tests/...

check: contracts-check lint typecheck test build vet

lint: lint-go lint-ts

lint-go:
	sh tests/tools/lint-go.sh run --config .golangci.yml $(GO_PACKAGES)

lint-ts:
	npm run lint

typecheck:
	npm run typecheck

test:
	go test $(GO_PACKAGES)
	npx vitest run

contracts:
	go run ./tests/tools/export-contracts

contracts-check:
	go run ./tests/tools/export-contracts -check

build:
	go build $(GO_PACKAGES)

vet:
	go vet $(GO_PACKAGES)
