package install

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// renewWithin is how close to its end a delegated key is reported, so the
// Owner runs `sisf up`, which issues new ones, before releases stop.
const renewWithin = 24 * time.Hour

// checkTopology checks what the supervisor releases through: its Docker
// request filters, the edge's admin listener, the runner images and its
// delegated keys, and whether the stages run at all.
func (in *Installer) checkTopology(ctx context.Context, c *collector, m *manifest.Manifest) {
	if m.Mode == manifest.ModeDocker {
		in.checkFilters(ctx, c, m)
		in.checkEdgeAdminDocker(ctx, c, m)
		in.checkRunnerImages(ctx, c, m)
		in.checkDelegations(c, m, func(name string) ([]byte, error) {
			return in.Exec.Run(ctx, Cmd{Name: "docker", Args: in.composeArgs(m, "exec", "-T", "supervisor", "cat", "/var/lib/sisf/trust/"+name)})
		})
	} else {
		in.checkEdgeAdminStandalone(ctx, c)
		c.ok("runner images", "Standalone builds candidates as local processes of the trusted simulation; no runner image is used")
		in.checkDelegations(c, m, dirReader(in.Layout.Data("trust")))
	}
	in.checkStages(c, m)
}

// filterPingVersion is a Docker API version both filter roles serve.
const filterPingVersion = "v1.44"

// checkFilters proves the Docker request filters are on the docker network
// with the supervisor and nothing else, that the supervisor reaches both,
// and that the agent zone reaches neither.
func (in *Installer) checkFilters(ctx context.Context, c *collector, m *manifest.Manifest) {
	proj := project(m.FactoryID)
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{
		"network", "inspect", "--format", "{{range .Containers}}{{.Name}} {{end}}", proj + "-" + dockerNetwork,
	}})
	if err != nil {
		c.fail("docker filters", "the docker network %s-%s does not exist: %s", proj, dockerNetwork, startHint)
		return
	}
	want := []string{proj + "-" + filterDeploy, proj + "-" + filterRunner, proj + "-supervisor"}
	got := strings.Fields(string(out))
	slices.Sort(got)
	if !slices.Equal(got, want) {
		c.fail("docker filters", "the docker network holds %v; it must hold the supervisor and the two filters only", got)
		return
	}
	for _, filter := range []string{filterRunner, filterDeploy} {
		url := "http://" + filter + ":" + strconv.Itoa(PortDockerFilter) + "/" + filterPingVersion + "/_ping"
		if _, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: in.composeArgs(m, "exec", "-T", "supervisor", "wget", "-q", "-T", "5", "-O", "/dev/null", url)}); err != nil {
			c.fail("docker filters", "the supervisor cannot reach %s", filter)
			return
		}
		if _, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: in.composeArgs(m, "exec", "-T", "dispatcher", "wget", "-q", "-T", "3", "-O", "/dev/null", url)}); err == nil {
			c.fail("docker filters", "the dispatcher, in the agent zone, reaches %s", filter)
			return
		}
	}
	c.ok("docker filters", "reachable by the supervisor only: the docker network holds it and the two filters, and the agent zone reaches neither")
}

// checkEdgeAdminDocker proves the edge's admin listener is published
// nowhere, refuses the supervisor without its token, and is out of the
// agent zone's reach.
func (in *Installer) checkEdgeAdminDocker(ctx context.Context, c *collector, m *manifest.Manifest) {
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{
		"inspect", "--format", "{{range $p, $b := .NetworkSettings.Ports}}{{range $b}}{{$p}}={{.HostIp}}:{{.HostPort}} {{end}}{{end}}",
		project(m.FactoryID) + "-edge",
	}})
	if err != nil {
		c.fail("edge admin", "the edge container cannot be inspected: %s", startHint)
		return
	}
	for _, binding := range strings.Fields(string(out)) {
		if binding != strconv.Itoa(PortEdge)+"/tcp=127.0.0.1:"+strconv.Itoa(PortEdge) {
			c.fail("edge admin", "the edge publishes %s; only its public port on loopback is published", binding)
			return
		}
	}
	admin := "http://" + edgeAdminAlias + ":" + strconv.Itoa(PortEdgeAdmin) + "/"
	// busybox wget fails on the refusal and names its status on stderr,
	// which the error carries.
	_, err = in.Exec.Run(ctx, Cmd{Name: "docker", Args: in.composeArgs(m, "exec", "-T", "supervisor", "wget", "-q", "-T", "5", "-O", "/dev/null", admin)})
	if err == nil || !strings.Contains(err.Error(), "401") {
		c.fail("edge admin", "the edge's admin listener does not refuse the supervisor without its token")
		return
	}
	if _, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: in.composeArgs(m, "exec", "-T", "dispatcher", "wget", "-q", "-T", "3", "-O", "/dev/null", admin)}); err == nil {
		c.fail("edge admin", "the dispatcher, in the agent zone, reaches the edge's admin listener")
		return
	}
	c.ok("edge admin", "not public: published nowhere, on the control network only, and refused without its token")
}

// checkEdgeAdminStandalone proves the edge's admin listener answers on
// loopback only and refuses a caller without its token.
func (in *Installer) checkEdgeAdminStandalone(ctx context.Context, c *collector) {
	code, _, err := in.httpGet(ctx, hostURL(PortEdgeAdmin, "/"), nil)
	switch {
	case err != nil:
		c.fail("edge admin", "the edge's admin listener does not answer on %s: %s", addr(PortEdgeAdmin), startHint)
	case code != http.StatusUnauthorized:
		c.fail("edge admin", "the edge's admin listener answered %d without its token", code)
	default:
		c.ok("edge admin", "not public: on %s only, and refused without its token", addr(PortEdgeAdmin))
	}
}

// checkRunnerImages proves the runner images pinned in the manifest are on
// this host.
func (in *Installer) checkRunnerImages(ctx context.Context, c *collector, m *manifest.Manifest) {
	var missing []string
	for _, unit := range []manifest.UnitName{manifest.UnitRunner, manifest.UnitQARunner} {
		digest := m.Units[unit].Digest
		if isUnbuilt(digest) {
			missing = append(missing, string(unit)+" (not built)")
			continue
		}
		if _, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: []string{"image", "inspect", "--format", "{{.Id}}", string(digest)}}); err != nil {
			missing = append(missing, string(unit))
		}
	}
	if len(missing) > 0 {
		c.fail("runner images", "not on this host: %s; run `sisf up` to build them", strings.Join(missing, ", "))
		return
	}
	c.ok("runner images", "the runner and QA runner images pinned in factory.yaml are present")
}

// checkDelegations proves the supervisor's delegated keys are on its trust
// volume, for this factory and its supervisor, and not expired.
func (in *Installer) checkDelegations(c *collector, m *manifest.Manifest, read trustReader) {
	delegations, err := readDelegations(read, m.FactoryID)
	if err != nil {
		c.fail("delegated keys", "%v; run `sisf up` to issue them", err)
		return
	}
	now := in.Clock.Now()
	var soonest time.Time
	for _, d := range delegations {
		if !d.NotAfter.After(now) {
			c.fail("delegated keys", "the %s key expired at %s; run `sisf up` to issue new ones", d.Purpose, d.NotAfter.Format(time.RFC3339))
			return
		}
		if soonest.IsZero() || d.NotAfter.Before(soonest) {
			soonest = d.NotAfter
		}
	}
	if soonest.Sub(now) < renewWithin {
		c.warn("delegated keys", "they expire at %s; run `sisf up` to issue new ones", soonest.Format(time.RFC3339))
		return
	}
	c.ok("delegated keys", "QA receipt and deploy artifact keys present, valid until %s", soonest.Format(time.RFC3339))
}

// checkStages says whether the supervisor builds, evaluates and releases.
func (in *Installer) checkStages(c *collector, m *manifest.Manifest) {
	o := Options{Sim: m.Extensions["profile"] == profileSim}
	o.QAAgent, _ = m.Extensions["qa_agent"].(string)
	o.QAAgentBinary, _ = m.Extensions["qa_agent_binary"].(string)
	choice := lifecycleChoice(o, m.Mode)
	if !choice.Enabled {
		c.warn("qa launcher", "%s", choice.Off)
		return
	}
	c.ok("lifecycle", "the supervisor builds, evaluates with the %s judge and releases candidates", choice.QAJudge)
}
