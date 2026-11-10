package install

import (
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/control"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The edge routes outside traffic to the control zone only on paths the
// control zone serves to the outside, in both modes and in what the manifest
// declares, so a path that nothing serves cannot be routed again.
func TestEveryPathTheEdgeRoutesIsAPublicPathOfTheControlZone(t *testing.T) {
	t.Parallel()
	public := control.PublicPaths()
	for _, mode := range []manifest.Mode{manifest.ModeStandalone, manifest.ModeDocker} {
		routed := map[string]string{}
		for name, u := range units(mode) {
			for _, path := range u.PublicRoutes {
				routed[path] = "the manifest's " + string(name) + " unit"
			}
		}
		for _, path := range edgeRoutes(t, mode) {
			routed[path] = "the edge's --route"
		}
		if len(routed) == 0 {
			t.Errorf("%s: the edge routes nothing to the control zone", mode)
		}
		for path, where := range routed {
			if !slices.Contains(public, path) {
				t.Errorf("%s: %s routes %s, which the control zone does not serve to the outside (%v)", mode, where, path, public)
			}
		}
	}
}

// edgeRoutes returns the path of each --route the edge of a fresh install of
// mode is started with: the process of a Standalone install, the service of
// the Compose project of a Docker one.
func edgeRoutes(t *testing.T, mode manifest.Mode) []string {
	t.Helper()
	var args []string
	if mode == manifest.ModeDocker {
		_, out := renderCompose(t, nil)
		command := composeFileOf(t, out).Services["edge"].Command
		must(t, command.Decode(&args))
	} else {
		in, _, _ := newTestInstaller(t)
		mustInit(t, in, mode)
		m, err := in.Layout.LoadManifest()
		must(t, err)
		units, closers, err := in.standaloneUnits(m)
		must(t, err)
		for _, c := range closers {
			t.Cleanup(func() { _ = c.Close() })
		}
		for _, u := range units {
			if u.name == "edge" {
				args = u.cmd.Args
			}
		}
		if args == nil {
			t.Fatal("the Standalone install has no edge process")
		}
	}
	var paths []string
	for i, arg := range args {
		spec, ok := strings.CutPrefix(arg, "--route=")
		if !ok && arg == "--route" && i+1 < len(args) {
			spec, ok = args[i+1], true
		}
		if ok {
			path, _, _ := strings.Cut(spec, "=")
			paths = append(paths, path)
		}
	}
	return paths
}
