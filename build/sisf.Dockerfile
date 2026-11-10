# sisf: the sisf command line, used for the one-shot state step and the fake GitHub of the simulation profile.
#
# Built by `sisf up` from the repository root:
#   docker build -f build/sisf.Dockerfile -t <project>-sisf:build .
# Both base images are pinned by the digest of their multi-platform index, so
# the same bytes are built on every host; moving a pin is a reviewed change.
# The program is a static binary (CGO_ENABLED=0) that runs as an unprivileged
# user, and the image holds no secret: credentials arrive at run time through
# the env file of the zone that runs it.

FROM golang:1.24-alpine@sha256:8bee1901f1e530bfb4a7850aa7a479d17ae3a18beb6e09064ed54cfd245b7191 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sisf ./cmd/sisf

FROM alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f
RUN addgroup -S -g 10001 sisf && adduser -S -u 10001 -G sisf -H -s /sbin/nologin sisf
COPY --from=build /out/sisf /usr/local/bin/sisf
USER 10001:10001
CMD ["sisf"]
