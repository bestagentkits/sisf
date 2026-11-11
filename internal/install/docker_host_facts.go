package install

import (
	"context"
	"net/netip"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// dockerSocket is the Docker daemon's socket as containers see it.
const dockerSocket = "/var/run/docker.sock"

// socketGroup asks the sisf image, with nothing but the socket mounted and
// no network, which group owns the Docker socket. The two filters are added
// to that group, and run as their unprivileged user otherwise.
func (in *Installer) socketGroup(ctx context.Context, m *manifest.Manifest, sisfImage string) (string, error) {
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{
		"run", "--rm", "--name", project(m.FactoryID) + "-socket-probe", "--network", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true", "-v", dockerSocket + ":" + dockerSocket + ":ro",
		"--entrypoint", "stat", sisfImage, "-c", "%g", dockerSocket,
	}})
	if err != nil {
		return "", unavailablef(err, "the group of the Docker socket could not be read")
	}
	gid := strings.TrimSpace(string(out))
	if !gidPattern.MatchString(gid) {
		return "", invalidf("the group of the Docker socket is not a number")
	}
	return gid, nil
}

// controlSubnets returns the IPv4 prefixes of the project's control
// network, comma separated, or "" when the network does not exist yet.
func (in *Installer) controlSubnets(ctx context.Context, m *manifest.Manifest) string {
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{
		"network", "inspect", "--format", "{{range .IPAM.Config}}{{.Subnet}} {{end}}", project(m.FactoryID) + "-control",
	}})
	if err != nil {
		return ""
	}
	var prefixes []string
	for _, f := range strings.Fields(string(out)) {
		if p, err := netip.ParsePrefix(f); err == nil && p.Addr().Is4() {
			prefixes = append(prefixes, p.Masked().String())
		}
	}
	return strings.Join(prefixes, ",")
}

// defaultSimPatch is the scripted agent's patch until a test or the Owner
// replaces it: a note added to the repository, which builds and releases
// as an ordinary change. A Standalone install outside the simulation whose
// coding agent is the scripted one gets it too.
const defaultSimPatch = `diff --git a/docs/sim-agent-note.md b/docs/sim-agent-note.md
new file mode 100644
--- /dev/null
+++ b/docs/sim-agent-note.md
@@ -0,0 +1 @@
+The scripted agent of the simulation profile left this note.
`

// ensureSimPatch writes the scripted agent's default patch when there is
// none. A patch already there, written by a test or the Owner, is kept.
func (in *Installer) ensureSimPatch() error {
	dir := in.Layout.SimAgentDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return unavailablef(err, "cannot create %s", dir)
	}
	path := dir + "/" + simPatchFile
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(defaultSimPatch), 0o644); err != nil {
		return unavailablef(err, "cannot write the scripted agent's patch")
	}
	return nil
}

// removeDeployed removes the containers the supervisor deployed for this
// factory, which Compose does not know of, so the project's networks can
// be removed after them. Only containers labelled with this factory are
// named.
func (in *Installer) removeDeployed(ctx context.Context, m *manifest.Manifest) error {
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{
		"ps", "-aq", "--filter", "label=sisf.factory=" + string(m.FactoryID),
	}})
	if err != nil {
		return unavailablef(err, "the factory's deployed containers could not be listed")
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	if _, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: append([]string{"rm", "-f"}, ids...)}); err != nil {
		return unavailablef(err, "the factory's deployed containers could not be removed")
	}
	return nil
}
