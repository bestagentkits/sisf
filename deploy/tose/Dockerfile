# SISF trial factory for a container platform without a Docker daemon (TOSE).
#
# Runs one Standalone factory in the container. `sisf up` rebuilds the
# factory from the checkout at every start, so the image keeps Go, git, make
# and the checkout. Every factory port stays on 127.0.0.1; nginx publishes
# only GET /healthz of the control zone on port 8080, so the dashboard, the
# REST API, MCP and the public report route are not reachable from outside.
#
# Build from the repository root:
#   docker build -f deploy/tose/Dockerfile -t sisf-trial .
# The image holds no secret: the GitHub token arrives at run time as GH_TOKEN.

FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59
RUN apk add --no-cache bash git make nginx github-cli ca-certificates tini \
 && addgroup -S -g 10001 sisf && adduser -S -u 10001 -G sisf -h /home/sisf -s /bin/bash sisf \
 && mkdir -p /data /opt/sisf && chown sisf:sisf /data /opt/sisf
USER 10001:10001
ENV GOPATH=/home/sisf/go GOFLAGS=-mod=readonly CGO_ENABLED=0 PATH=/home/sisf/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
WORKDIR /opt/sisf/src
COPY --chown=10001:10001 go.mod go.sum ./
RUN go mod download
COPY --chown=10001:10001 . .
# `make install` stamps the version from git; the build context may carry no
# .git, so the version and commit can be passed as build arguments.
ARG SISF_VERSION=trial
ARG SISF_COMMIT=unknown
RUN make install VERSION="$SISF_VERSION" COMMIT="$SISF_COMMIT" && sisf version
EXPOSE 8080
ENTRYPOINT ["/sbin/tini", "--", "/opt/sisf/src/deploy/tose/entrypoint.sh"]
