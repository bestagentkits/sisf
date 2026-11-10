.PHONY: build test lint e2e smoke up down

ARGS ?=

build:
	CGO_ENABLED=0 go build ./...

test:
	CGO_ENABLED=1 go test ./... -race

lint:
	golangci-lint run

e2e:
	go test -tags e2e ./tests/e2e/... -count=1 -timeout 45m

smoke:
	go test -tags smoke ./tests/smoke/... -count=1

up:
	go run ./cmd/sisf up $(ARGS)

down:
	go run ./cmd/sisf down $(ARGS)
