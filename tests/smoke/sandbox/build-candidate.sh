#!/bin/sh
# The smoke run's candidate build, run by the supervisor in a runner sandbox
# (--build-command /usr/local/bin/sisf-smoke-build). It gets the candidate's
# commit in SISF_COMMIT and nothing secret: the private sandbox repository
# is fetched from the run's read-only mirror on the sandbox network, which
# the harness keeps in step with GitHub using the operator's gh login on the
# host. It tests and builds every Go package and reports, on the lines the
# supervisor reads, whether the tests passed, the digest of what it built and
# the digests of the SBOM and provenance it wrote for that build; the
# supervisor's observed-build hard gate fails a build that names neither.
# The digest names the built programs only: Standalone's local deployment
# backend starts the installed application, not this build.
set -eu
: "${SISF_COMMIT:?the supervisor sets SISF_COMMIT}"
mirror=${SISF_SMOKE_MIRROR:?}
work=${SISF_SMOKE_WORKDIR:-/tmp}
src=$work/src
out=$work/out
meta=$work/meta
mkdir -p "$src" "$out" "$meta"
cd "$src"
git init -q
# The mirror follows GitHub every few seconds; a candidate pushed a moment
# ago may not be there yet.
tries=0
until git fetch -q --depth=1 "$mirror" "$SISF_COMMIT" 2>/dev/null; do
	tries=$((tries + 1))
	if [ "$tries" -ge 60 ]; then
		echo "the commit $SISF_COMMIT is not on the mirror $mirror" >&2
		exit 1
	fi
	sleep 5
done
git checkout -q FETCH_HEAD
# The Go module is the repository's root, or else its one directory with a
# go.mod (the sandbox may hold the example application below examples/).
if [ ! -f go.mod ]; then
	modules=$(find . -name go.mod -not -path './.*' | sed 's|/go.mod$||')
	if [ "$(printf '%s\n' "$modules" | grep -c .)" -ne 1 ]; then
		echo "the candidate has no single Go module to build: ${modules:-none}" >&2
		exit 1
	fi
	cd "$modules"
fi
tests=passed
go vet ./... >&2 || tests=failed
go test ./... >&2 || tests=failed
go build -trimpath -o "$out/" ./... >&2
digest=$(cd "$out" && find . -type f | LC_ALL=C sort | xargs sha256sum | sha256sum | cut -d' ' -f1)
# The SBOM is the module list Go recorded in every built program; the
# provenance names the commit, the toolchain and this builder. Each is
# referenced by the digest of what was written, outside the digested output.
(cd "$out" && find . -type f | LC_ALL=C sort | xargs go version -m) >"$meta/sbom.txt"
printf 'commit %s\nbuilder sisf-smoke-build\ntoolchain %s\nimage-digest sha256:%s\n' \
	"$SISF_COMMIT" "$(go env GOVERSION)" "$digest" >"$meta/provenance.txt"
sbom=$(sha256sum "$meta/sbom.txt" | cut -d' ' -f1)
provenance=$(sha256sum "$meta/provenance.txt" | cut -d' ' -f1)
echo "sisf-tests: $tests"
echo "sisf-image-digest: sha256:$digest"
echo "sisf-image: smoke.local/sisf-sandbox@sha256:$digest"
echo "sisf-sbom: go-version-m:sha256:$sbom"
echo "sisf-provenance: sisf-smoke-build:sha256:$provenance"
