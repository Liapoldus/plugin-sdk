.PHONY: check test build vet

check: test build vet

test:
	npx vitest run

build:
	go build ./...

vet:
	go vet ./...
