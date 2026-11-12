# ADR 0019: Go 1.25 toolchain

## Status

Status: accepted. Supersedes the earlier choice of Go 1.24 for the module and the build images.

## Context

Go 1.24 is past upstream support, so it no longer receives security fixes. Fixes for advisories in the approved dependencies (`github.com/jackc/pgx/v5`, `golang.org/x/text`, `golang.org/x/sys`) ship only in releases that declare `go 1.25` or later.

## Decision

- `go.mod` declares `go 1.25.14`, with no separate `toolchain` line.
- The build images use `golang:1.25-alpine`, pinned by its multi-arch index digest.
- Dependencies stay on the newest releases that still declare `go 1.25.x`. Releases that require Go 1.26 are not adopted until a later toolchain decision.
- CI keeps reading the version from `go.mod` (`go-version-file`), so it needs no separate pin.

## Consequences

- Contributors need Go 1.25.14 or newer. `GOTOOLCHAIN=auto` fetches it when the local toolchain is older.
- The image digest is refreshed whenever the pinned image is rebuilt, as for every other pinned image.
- `govulncheck ./...` reports no reachable vulnerabilities after the update.

## Acceptance test

`go build ./...`, `go vet ./...` and `go test ./... -race` pass on Go 1.25, `govulncheck ./...` reports no affected code, and the install test still rejects a Dockerfile whose `FROM` is not pinned by digest.
