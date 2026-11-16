#!/bin/sh
# The smoke run's candidate build, run by the supervisor in a runner sandbox
# (--build-command /usr/local/bin/sisf-smoke-build), once per phase, each
# time in a fresh container, with the phase in SISF_PHASE:
#
#   test    vets and tests every Go package of the candidate (SISF_COMMIT);
#           the supervisor reads only the exit status.
#   build   builds every package for the supervisor's operating system and
#           architecture (SISF_TARGET_OS, SISF_TARGET_ARCH); the supervisor
#           reads only the exit status.
#   report  builds the one program of the commit the supervisor names
#           (SISF_REPORT_COMMIT) for the supervisor's own operating system
#           and architecture (SISF_REPORT_TARGET_OS, SISF_REPORT_TARGET_ARCH),
#           which runs none of its code, and reports on lines
#           "sisf-report <nonce> <key>: <value>" the sha256 of that program
#           as the image digest, and the program itself, in base64 on
#           "program" lines. It reports no SBOM or provenance: the
#           supervisor makes both from the program it receives.
#
# The program is what a Standalone factory deploys: the supervisor keeps
# the bytes it receives, checks them against the digest and its local
# deployment backend runs exactly that file. A candidate is one program; a
# commit with none or several is refused where it is built. The
# application's synthetic suite is not built here: it comes from the
# factory's own source, never from a candidate.
#
# It gets nothing secret: the private sandbox repository is fetched from the
# run's read-only mirror on the sandbox network, which the harness keeps in
# step with GitHub using the operator's gh login on the host.
set -eu
phase=${SISF_PHASE:?the supervisor sets SISF_PHASE}
case $phase in
test) commit=${SISF_COMMIT:?the supervisor sets SISF_COMMIT} ;;
build)
	commit=${SISF_COMMIT:?the supervisor sets SISF_COMMIT}
	target_os=${SISF_TARGET_OS:?the supervisor sets SISF_TARGET_OS}
	target_arch=${SISF_TARGET_ARCH:?the supervisor sets SISF_TARGET_ARCH}
	case $target_os$target_arch in
	*[!a-z0-9]*)
		echo "the target $target_os/$target_arch is not an operating system and architecture" >&2
		exit 2
		;;
	esac
	;;
report)
	commit=${SISF_REPORT_COMMIT:?the supervisor sets SISF_REPORT_COMMIT}
	nonce=${SISF_REPORT_NONCE:?the supervisor sets SISF_REPORT_NONCE}
	target_os=${SISF_REPORT_TARGET_OS:?the supervisor of a Standalone factory sets SISF_REPORT_TARGET_OS}
	target_arch=${SISF_REPORT_TARGET_ARCH:?the supervisor of a Standalone factory sets SISF_REPORT_TARGET_ARCH}
	case $target_os$target_arch in
	*[!a-z0-9]*)
		echo "the target $target_os/$target_arch is not an operating system and architecture" >&2
		exit 2
		;;
	esac
	;;
*)
	echo "unknown phase $phase" >&2
	exit 2
	;;
esac
mirror=${SISF_SMOKE_MIRROR:?}
work=${SISF_SMOKE_WORKDIR:-/tmp}
src=$work/src
out=$work/out
mkdir -p "$src" "$out"
cd "$src"
git init -q
# The mirror follows GitHub every few seconds; a candidate pushed a moment
# ago may not be there yet.
tries=0
until git fetch -q --depth=1 "$mirror" "$commit" 2>/dev/null; do
	tries=$((tries + 1))
	if [ "$tries" -ge 60 ]; then
		echo "the commit $commit is not on the mirror $mirror" >&2
		exit 1
	fi
	sleep 5
done
git checkout -q FETCH_HEAD
# The Go module is the repository's root, or else its one directory with a
# go.mod (the sandbox may hold the example application below examples/).
# The go.mod files are counted one character each, so a directory name with
# a space or a line break in it can neither split nor join two of them.
if [ ! -f go.mod ]; then
	count=$(find . -name go.mod -not -path './.*' -exec printf x \; | wc -c | tr -d ' ')
	if [ "$count" -ne 1 ]; then
		echo "the candidate has $count Go modules; it needs exactly one to build" >&2
		exit 1
	fi
	module=$(find . -name go.mod -not -path './.*' -exec dirname {} \;)
	if [ ! -f "$module/go.mod" ]; then
		echo "the directory of the candidate's Go module cannot be named safely" >&2
		exit 1
	fi
	cd "$module"
fi
if [ "$phase" = test ]; then
	go vet ./... >&2
	go test ./... >&2
	exit 0
fi
# A unit is one program: the commit's one main package.
programs=$(go list -f '{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./... | grep -c . || true)
if [ "$programs" -ne 1 ]; then
	echo "the candidate has $programs programs (main packages); a unit is exactly one" >&2
	exit 1
fi
program=$(go list -f '{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./... | grep .)
# Building compiles the candidate and runs none of it: no cgo, no generate.
if [ "$phase" = build ]; then
	CGO_ENABLED=0 GOOS=$target_os GOARCH=$target_arch go build -trimpath -o "$out/" ./... >&2
	exit 0
fi
rm -f "$out/program"
CGO_ENABLED=0 GOOS=$target_os GOARCH=$target_arch go build -trimpath -o "$out/program" "$program" >&2
# A build that wrote no program has nothing to report: the digest of an
# absent or empty file names no program.
if [ ! -s "$out/program" ]; then
	echo "the build produced no program at $out/program" >&2
	exit 1
fi
digest=$(sha256sum "$out/program" | cut -d' ' -f1)
case $digest in
[0-9a-f]*) ;;
*)
	echo "the program's digest cannot be computed" >&2
	exit 1
	;;
esac
if [ ${#digest} -ne 64 ]; then
	echo "the program's digest cannot be computed" >&2
	exit 1
fi
echo "sisf-report $nonce image-digest: sha256:$digest"
echo "sisf-report $nonce image: smoke.local/sisf-sandbox@sha256:$digest"
# The program, in lines of base64 that each decode by themselves (4096 is
# a multiple of four).
base64 <"$out/program" | tr -d '\n' | fold -w 4096 |
	awk -v prefix="sisf-report $nonce program: " 'length($0) { print prefix $0 }'
