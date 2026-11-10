# runner: the sandbox a candidate build runs in: the Go toolchain and nothing else, no secret and no network but the egress proxy the supervisor's runner filter attaches it to.
#
# Built by `sisf up` from the repository root:
#   docker build -f build/runner.Dockerfile -t <project>-runner:build .
# The base image is pinned by the digest of its multi-platform index, so the
# same bytes are built on every host; moving a pin is a reviewed change. It
# runs as an unprivileged user with a read-only root file system: the
# toolchain's caches live on /tmp, and the workspace is mounted by the
# supervisor's execution backend. Programs it builds are static
# (CGO_ENABLED=0), and the image holds no secret.

FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59
LABEL org.opencontainers.image.title="sisf-runner"
RUN addgroup -S -g 10001 sisf && adduser -S -u 10001 -G sisf -H -s /sbin/nologin sisf
RUN mkdir -p /workspace && chown sisf:sisf /workspace
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly GOCACHE=/tmp/go-cache GOPATH=/tmp/go GOTOOLCHAIN=local
WORKDIR /workspace
USER 10001:10001
CMD ["go", "version"]
