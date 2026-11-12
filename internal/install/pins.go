package install

import (
	"fmt"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Third-party images are pinned by the digest of their multi-platform index,
// so the same bytes run on every host. The tag is kept beside the digest to
// tell a reviewer what is pinned; Docker resolves by the digest alone.
// Moving a pin is a reviewed change to this file.
const (
	// PostgresImage is the state store of Docker mode.
	PostgresImage  = "postgres:17.11-alpine@" + PostgresDigest
	PostgresDigest = "sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac"
)

// Artifacts that `sisf up` builds, in the order their placeholders count.
const (
	artifactApp        = "app"
	artifactFactoryd   = "factoryd"
	artifactSupervisor = "supervisor"
	artifactRunner     = "runner"
	artifactQARunner   = "qa-runner"
	artifactEdge       = "edge"
)

var artifactOrder = []string{artifactApp, artifactFactoryd, artifactSupervisor, artifactRunner, artifactQARunner, artifactEdge}

// unbuilt stands in for the digest of an artifact that has not been built
// yet. It has the shape of a digest, differs for every artifact, and
// matches nothing real, so the manifest stays complete for the checker (which
// rejects two artifacts sharing a digest with the supervisor) while `up`
// refuses it and the doctor reports it.
func unbuilt(artifact string) core.Digest {
	n := 0
	for i, a := range artifactOrder {
		if a == artifact {
			n = i + 1
		}
	}
	return core.Digest(fmt.Sprintf("sha256:%063d%x", 0, n))
}

// isUnbuilt reports whether d is a placeholder.
func isUnbuilt(d core.Digest) bool {
	rest, ok := strings.CutPrefix(string(d), "sha256:")
	return d == "" || ok && len(rest) == 64 && strings.Trim(rest[:63], "0") == ""
}

// digestPinned reports whether an image reference names its content: a
// digest after "@", or a bare image ID, which Docker resolves locally.
func digestPinned(image string) bool {
	if rest, ok := strings.CutPrefix(image, "sha256:"); ok {
		return len(rest) == 64 && isHex(rest)
	}
	_, digest, ok := strings.Cut(image, "@sha256:")
	return ok && len(digest) == 64 && isHex(digest)
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
