# supervisor: the trusted release supervisor; it measures its own executable with `supervisor digest`.
#
# Built by `sisf up` from the repository root:
#   docker build -f build/supervisor.Dockerfile -t <project>-supervisor:build .
# Both base images are pinned by the digest of their multi-platform index, so
# the same bytes are built on every host; moving a pin is a reviewed change.
# The program is a static binary (CGO_ENABLED=0) that runs as an unprivileged
# user, and the image holds no secret: credentials arrive at run time through
# the env file of the zone that runs it.

FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/supervisor ./cmd/supervisor

FROM alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f
RUN addgroup -S -g 10001 sisf && adduser -S -u 10001 -G sisf -H -s /sbin/nologin sisf
RUN mkdir -p /var/lib/sisf/trust && chown -R sisf:sisf /var/lib/sisf/trust && chmod 700 /var/lib/sisf/trust
COPY --from=build /out/supervisor /usr/local/bin/supervisor
USER 10001:10001
CMD ["supervisor"]
