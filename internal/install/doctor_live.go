package install

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// hostURL is a loopback URL of a published port.
func hostURL(port int, path string) string { return "http://" + addr(port) + path }

// startHint ends the failure of every check that needs a running factory.
const startHint = "start the factory with `sisf up`"

// checkBudget reports whether the Owner has confirmed the budget. Without a
// confirmation the factory takes in issues and starts no work.
func (in *Installer) checkBudget(c *collector, _ *manifest.Manifest) {
	rec, ok, err := readBudgetConfirmation(in.Layout.BudgetConfirmation())
	switch {
	case err != nil:
		c.fail("budget", "%v", err)
	case !ok:
		c.warn("budget", "not confirmed: the factory takes in issues but starts no work; confirm with `sisf up --confirm-unlimited`")
	case rec.AlertOff:
		c.ok("budget", "unlimited, confirmed, no alert")
	default:
		c.ok("budget", "unlimited, confirmed, alert above %g", float64(rec.AlertThresholdMicros)/microsPerUnit)
	}
}

// checkZones asks the control plane for the verdict on all three zones, then
// checks the path a visitor takes and the dispatcher.
func (in *Installer) checkZones(ctx context.Context, c *collector, m *manifest.Manifest) {
	code, body, err := in.httpGet(ctx, hostURL(PortControl, "/healthz"), nil)
	var health struct {
		Zones map[core.Zone]string `json:"zones"`
	}
	switch {
	case err != nil:
		c.fail("zones", "the control plane does not answer on %s: %s", addr(PortControl), startHint)
	case json.Unmarshal(body, &health) != nil || len(health.Zones) == 0:
		c.fail("zones", "the control plane answered %d with no zone verdicts", code)
	default:
		for _, z := range core.Zones() {
			if got := health.Zones[z]; got == "ok" {
				c.ok("zone "+string(z), "healthy%s", zoneNote(z))
			} else {
				c.fail("zone "+string(z), "reported %q", got)
			}
		}
	}
	in.checkGet(ctx, c, "edge to app", hostURL(PortEdge, "/healthz"), "the app answers through the edge")
	if m.Mode == manifest.ModeStandalone {
		in.checkGet(ctx, c, "dispatcher", hostURL(PortDispatcher, "/healthz"), "answers")
		return
	}
	in.checkContainers(ctx, c, m)
}

// zoneNote says what a healthy verdict covers beyond the zone's own process.
func zoneNote(z core.Zone) string {
	if z == core.ZoneControl {
		return ": the state store is open and answering"
	}
	return ""
}

// checkGet reports whether a URL answers 200.
func (in *Installer) checkGet(ctx context.Context, c *collector, name, url, okText string) {
	switch code, _, err := in.httpGet(ctx, url, nil); {
	case err != nil:
		c.fail(name, "no answer: %s", startHint)
	case code != http.StatusOK:
		c.fail(name, "answered %d", code)
	default:
		c.ok(name, "%s", okText)
	}
}

// runningService is the part of `docker compose ps` the doctor reads.
type runningService struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

// checkContainers checks that every service of the project is running and,
// where it has a health check, healthy. Zones 1 and 2 are not published to
// the host, so this is how the doctor sees them in Docker mode.
func (in *Installer) checkContainers(ctx context.Context, c *collector, m *manifest.Manifest) {
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: in.composeArgs(m, "ps", "--format", "json")})
	if err != nil {
		c.fail("containers", "Docker Compose does not answer for project %s: %s", project(m.FactoryID), startHint)
		return
	}
	services, err := parseComposePS(out)
	if err != nil || len(services) == 0 {
		c.fail("containers", "project %s has no containers: %s", project(m.FactoryID), startHint)
		return
	}
	for _, s := range services {
		name := "container " + s.Service
		switch {
		case s.State != "running":
			c.fail(name, "is %s", s.State)
		case s.Health != "" && s.Health != "healthy":
			c.fail(name, "is %s", s.Health)
		default:
			c.ok(name, "running")
		}
	}
}

// parseComposePS reads the output of `docker compose ps --format json`,
// which is one JSON object per line in current releases and one array in
// older ones.
func parseComposePS(out []byte) ([]runningService, error) {
	text := strings.TrimSpace(string(out))
	if strings.HasPrefix(text, "[") {
		var all []runningService
		return all, json.Unmarshal([]byte(text), &all)
	}
	var all []runningService
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var s runningService
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			return nil, err
		}
		all = append(all, s)
	}
	return all, nil
}

// checkGitHub proves the token can reach the repository and, when GitHub
// reports permissions, that it may write to it.
func (in *Installer) checkGitHub(ctx context.Context, c *collector, m *manifest.Manifest) {
	raw, err := os.ReadFile(in.Layout.Secret(areaControl, "github-token"))
	if err != nil {
		c.fail("github", "the GitHub token cannot be read from the control zone's secrets")
		return
	}
	api, _ := m.Extensions["github_api_url"].(string)
	if err := checkGitHubURL(api); err != nil {
		c.fail("github", "%v", err)
		return
	}
	token := core.NewSecretValue([]byte(strings.TrimSpace(string(raw))))
	url := strings.TrimRight(api, "/") + "/repos/" + m.Repository.Owner + "/" + m.Repository.Repo
	code, header, body, err := in.httpProbe(ctx, url, http.Header{
		"Authorization": {"Bearer " + string(token.Reveal())},
		"Accept":        {"application/vnd.github+json"},
	})
	repo := m.Repository.Owner + "/" + m.Repository.Repo
	var reply struct {
		Permissions *struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	switch {
	case err != nil:
		c.fail("github", "%s does not answer; check the API address and your network", api)
	case code == http.StatusUnauthorized:
		c.fail("github", "the token was refused: it is wrong, expired or revoked")
	case code == http.StatusNotFound || code == http.StatusForbidden:
		c.fail("github", "the token cannot see %s: grant the app access to the repository (answer %d)", repo, code)
	case code != http.StatusOK:
		c.fail("github", "answered %d for %s", code, repo)
	case json.Unmarshal(body, &reply) == nil && reply.Permissions != nil && !reply.Permissions.Push:
		c.fail("github", "the token can read %s but not write: issues, comments and pull requests need write access", repo)
	case reply.Permissions == nil:
		c.ok("github", "%s is reachable; GitHub did not report the token's permissions", repo)
	default:
		c.ok("github", "%s is reachable and the token may write to it", repo)
	}
	if code == http.StatusOK {
		checkTokenScopes(c, header)
	}
}

// checkTokenScopes warns when a classic token carries the workflow scope.
// The supervisor pushes agents' candidates to sisf/** branches with this
// token; with the workflow scope a candidate that slipped past the
// builder's protected-path refusal could add or change a workflow. GitHub
// reports a classic token's scopes in X-OAuth-Scopes; fine-grained and app
// tokens send no such header, and then nothing is said.
func checkTokenScopes(c *collector, header http.Header) {
	raw, ok := header["X-Oauth-Scopes"]
	if !ok {
		return
	}
	for _, scope := range strings.Split(strings.Join(raw, ","), ",") {
		if strings.TrimSpace(scope) == "workflow" {
			c.warn("github token scope", "the token carries the workflow scope, so code pushed with it can change CI workflows; issue a token without it")
			return
		}
	}
	c.ok("github token scope", "the token does not carry the workflow scope")
}

// checkRollback proves the supervisor is running its self-check and that a
// release exists to roll back to.
func (in *Installer) checkRollback(ctx context.Context, c *collector, m *manifest.Manifest) {
	status, err := in.supervisorStatus(ctx, m)
	switch {
	case err != nil:
		c.fail("rollback", "the supervisor does not answer, so nothing could be rolled back: %s", startHint)
	case !status.SelfCheck.Passed:
		c.fail("rollback", "the supervisor's self-check has not passed")
	case !status.LKG.Recorded:
		c.warn("rollback", "no last known good release is recorded yet; a rollback has nothing to return to until the first release is promoted")
	default:
		c.ok("rollback", "supervisor verified; last known good release recorded %s", status.LKG.RecordedAt.Format("2006-01-02 15:04 MST"))
	}
}

// supervisorStatus reads the supervisor's status. In Docker mode the admin
// socket is inside the container, so the supervisor's own client is run there.
func (in *Installer) supervisorStatus(ctx context.Context, m *manifest.Manifest) (supervisor.Status, error) {
	admin := addr(PortSupervisor)
	if m.Mode == manifest.ModeStandalone {
		if in.SupervisorStatus != nil {
			return in.SupervisorStatus(ctx, admin)
		}
		return supervisor.FetchStatus(ctx, admin)
	}
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: in.composeArgs(m, "exec", "-T", "supervisor", "supervisor", "status", "--admin="+admin)})
	if err != nil {
		return supervisor.Status{}, err
	}
	var status supervisor.Status
	return status, json.Unmarshal(out, &status)
}
