package appbundle

import (
	"fmt"
	"path"
	"strings"
)

// checkRelative refuses a slash-separated path that is empty, absolute,
// unclean, contains a backslash or a NUL, or climbs out with "..". It is the
// one rule for every name taken from a Spec or an archive.
func checkRelative(what, p string) error {
	switch {
	case p == "" || p == ".":
		return fmt.Errorf("%s is empty", what)
	case strings.ContainsAny(p, "\\\x00"):
		return fmt.Errorf("%s %q contains a backslash or NUL", what, p)
	case path.IsAbs(p):
		return fmt.Errorf("%s %q is absolute", what, p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return fmt.Errorf("%s %q climbs out with a .. component", what, p)
		}
	}
	if path.Clean(p) != p {
		return fmt.Errorf("%s %q is not a clean path", what, p)
	}
	return nil
}
