// Package install creates, starts, checks and stops one factory deployment on
// one machine, in Standalone mode (a supervised process group with SQLite) or
// in Docker mode (one Compose project with PostgreSQL).
//
// A directory holds exactly one install: factory.yaml, the signed release
// manifest, the secrets (owner-only, one area per zone) and, in Standalone
// mode, the programs, data and logs. The package acts on that directory and
// on nothing else; every Docker object it creates is named "sisf-<factory>"
// so it can be told apart from anything else on the host.
//
// Everything that reaches outside the process (building, Docker, the clock,
// the network) is a field of Installer, so the default test run starts no
// program and needs no Docker.
//
// # Throwaway PostgreSQL for the state store tests
//
// The state store suites (pkg/state/..., internal/audit/...) and the
// PostgreSQL case of this package's bootstrap test run against a real server
// only when SISF_TEST_PG_DSN names one; without it they skip. The project
// owns host ports 7460-7469, and 7469 is the one for a PostgreSQL debug
// server, bound to loopback only:
//
//	docker run -d --name sisf-test-pg -p 127.0.0.1:7469:5432 \
//	    -e POSTGRES_PASSWORD=test-only -e POSTGRES_DB=sisf postgres:16-alpine
//	export SISF_TEST_PG_DSN='postgres://postgres:test-only@127.0.0.1:7469/sisf?sslmode=disable'
//	go test ./pkg/state/... ./internal/audit/... ./internal/install/... -count=1
//	docker rm -f sisf-test-pg
//
// The password above is a placeholder for a server that exists for minutes.
// Name the container with the "sisf-" prefix and remove it afterwards.
// Zone isolation checks that need Docker live under the e2e build tag.
//
// # Docker API access
//
// No service of the Compose project has Docker API access: there is no socket
// mount, no DOCKER_HOST and no socket proxy, and CheckCompose rejects each.
// Container creation for agent runs must go through a filtering proxy that
// denies inspect of every container except runner containers and allows only
// creates with no bind mounts, no privileges and the sandbox network. That
// proxy must ship with a live negative test (inspect of the control
// container, and a privileged or bind-mounting create, are both refused)
// before any Docker API access is given back to Zone 2.
package install
