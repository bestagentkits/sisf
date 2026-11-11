//go:build smoke

package smoke

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The loopback ports this run may use. The install binds a subset of them;
// all must be free before anything starts.
const firstPort, lastPort = 7460, 7469

// preflightFacts is what the preflight learned and the run needs later.
type preflightFacts struct {
	login   string // the gh account, to count its activity elsewhere; never reported
	ghToken string // never logged; seeds the scrubber
}

// preflight checks every precondition and changes nothing: not GitHub, not
// the machine. Any failure ends the run before a side effect.
func preflight(ctx context.Context, r *commandRunner) preflightFacts {
	r.t.Helper()
	const limit = time.Minute
	var facts preflightFacts

	// The token first, so that anything printed afterwards is redacted.
	token, err := r.run(ctx, limit, nil, "gh", "auth", "token")
	if err != nil || strings.TrimSpace(token) == "" {
		r.fatalf("preflight: `gh auth token` gave no token; log in with `gh auth login`: %v", err)
	}
	facts.ghToken = strings.TrimSpace(token)
	r.scrub.addSecret(facts.ghToken)

	if _, err := r.run(ctx, limit, nil, "gh", "auth", "status"); err != nil {
		r.fatalf("preflight: gh is not logged in: %v", err)
	}
	out, err := r.run(ctx, limit, nil, "gh", "repo", "view", sandboxRepo, "--json", "visibility,nameWithOwner")
	if err != nil {
		r.fatalf("preflight: the sandbox %s cannot be read: %v", sandboxRepo, err)
	}
	var repo struct{ Visibility, NameWithOwner string }
	if err := json.Unmarshal([]byte(out), &repo); err != nil {
		r.fatalf("preflight: gh repo view answered something that is not JSON: %v", err)
	}
	if repo.NameWithOwner != sandboxRepo || repo.Visibility != "PRIVATE" {
		r.fatalf("preflight: %s must be private and named exactly so; gh shows %q with visibility %q",
			sandboxRepo, repo.NameWithOwner, repo.Visibility)
	}
	r.report.check("sandbox is private", statusPass, repo.NameWithOwner+" "+repo.Visibility)

	out, err = r.run(ctx, limit, nil, "gh", "label", "list", "--repo", sandboxRepo, "--json", "name", "--limit", "500")
	if err != nil {
		r.fatalf("preflight: the labels of %s cannot be listed: %v", sandboxRepo, err)
	}
	var labels []struct{ Name string }
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		r.fatalf("preflight: gh label list answered something that is not JSON: %v", err)
	}
	for _, want := range issueLabels {
		if !slices.ContainsFunc(labels, func(l struct{ Name string }) bool { return l.Name == want }) {
			r.fatalf("preflight: the sandbox has no label %q; create it before the run", want)
		}
	}
	// Policy C reads the foundation files from the sandbox's default branch.
	if _, err := r.run(ctx, limit, nil, "gh", "api", "repos/"+sandboxRepo+"/contents/.sisf/factory.yaml", "--jq", ".path"); err != nil {
		r.fatalf("preflight: the sandbox has no .sisf/factory.yaml on its default branch; seed it with examples/hello-app "+
			"and the foundation files first: %v", err)
	}
	login, err := r.run(ctx, limit, nil, "gh", "api", "user", "--jq", ".login")
	if err != nil || strings.TrimSpace(login) == "" {
		r.fatalf("preflight: the gh account cannot be read: %v", err)
	}
	facts.login = strings.TrimSpace(login)
	// The login is personal data: the log and the report name a placeholder.
	r.scrub.addPersonal(facts.login, "<gh-login>")

	for _, tool := range [][]string{{"gh", "--version"}, {"claude", "--version"}, {"codex", "--version"}, {"docker", "version", "--format", "{{.Server.Version}}"}, {"go", "version"}, {"git", "rev-parse", "HEAD"}} {
		out, err := r.run(ctx, limit, nil, tool[0], tool[1:]...)
		if err != nil {
			r.fatalf("preflight: `%s` failed: %v", strings.Join(tool, " "), err)
		}
		name := tool[0]
		if name == "git" {
			name = "sisf source revision"
		}
		r.report.Versions = append(r.report.Versions, namedValue{Name: name, Value: r.scrub.clean(firstLine(out))})
	}
	if busy := busyPorts(); len(busy) > 0 {
		r.fatalf("preflight: ports %v on 127.0.0.1 are in use; the run needs %d-%d free (is another factory up?)", busy, firstPort, lastPort)
	}
	return facts
}

// busyPorts lists the run's ports that something answers on or holds.
func busyPorts() []int {
	var busy []int
	for port := firstPort; port <= lastPort; port++ {
		address := "127.0.0.1:" + strconv.Itoa(port)
		if conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond); err == nil {
			_ = conn.Close()
			busy = append(busy, port)
			continue
		}
		ln, err := net.Listen("tcp", address)
		if err != nil {
			busy = append(busy, port)
			continue
		}
		_ = ln.Close()
	}
	return busy
}
