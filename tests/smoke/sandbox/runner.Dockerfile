# The runner image of the smoke run's build sandbox: the product's runner
# (build/runner.Dockerfile, the same pinned toolchain) plus git, to fetch the
# candidate from the run's read-only mirror, and the build script the
# supervisor runs as its --build-command. It holds no secret.
#
# Built and pushed to the run's loopback registry by the smoke harness
# (tests/smoke/build_sandbox_test.go), so the docker backend can run it
# pinned by its registry digest.
FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59
LABEL org.opencontainers.image.title="sisf-smoke-runner"
RUN apk add --no-cache git git-daemon
RUN addgroup -S -g 10001 sisf && adduser -S -u 10001 -G sisf -H -s /sbin/nologin sisf
RUN mkdir -p /workspace && chown sisf:sisf /workspace
COPY build-candidate.sh /usr/local/bin/sisf-smoke-build
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly GOCACHE=/tmp/go-cache GOPATH=/tmp/go GOTOOLCHAIN=local \
    SISF_SMOKE_MIRROR=git://git-mirror/sandbox.git
WORKDIR /workspace
USER 10001:10001
CMD ["go", "version"]
