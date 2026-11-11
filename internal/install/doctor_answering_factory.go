package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// runningCheck names the doctor line that says whether this install is the
// factory answering on its ports.
const runningCheck = "running"

// answering is what the doctor learned about who answers on the install's
// fixed loopback ports. Two installs on one host share those ports, so an
// answer counts as this install's only when it proves it: a running process
// or Compose project of this install, the factory id of this install, and,
// from the supervisor, the root key of this install.
type answering struct {
	// ours is true when the live checks talk to this install's factory.
	ours bool
	// skip says why the live checks were not run when ours is false.
	skip string
}

// identity of what answered one probe; empty fields were not reported.
type answeredBy struct {
	port      string
	factoryID string
	keyID     string
	reached   bool
}

// checkAnswering decides, before any live check runs, whether the units on
// the fixed ports are this install's. It adds one line to the report and
// returns what the live checks should do.
func (in *Installer) checkAnswering(ctx context.Context, c *collector, m *manifest.Manifest) answering {
	running, why := in.instanceRunning(ctx, m)
	control := in.controlIdentity(ctx)
	sup := in.supervisorIdentity(ctx, m)
	ownKey := in.rootKeyID()
	foreign := func(a answeredBy) string {
		switch {
		case !a.reached:
			return ""
		case a.factoryID == "":
			return a.port + " is answered by a program that does not say which factory it belongs to"
		case a.factoryID != string(m.FactoryID):
			return a.port + " is answered by factory " + a.factoryID + ", not this install's " + string(m.FactoryID)
		case a.keyID != "" && ownKey != "" && a.keyID != ownKey:
			return a.port + " is answered by another install of factory " + a.factoryID + ": it names root key " + a.keyID + ", not this install's"
		}
		return ""
	}
	var others []string
	for _, a := range []answeredBy{control, sup} {
		if text := foreign(a); text != "" {
			others = append(others, text)
		}
	}
	startCmd := "start it with `sisf up --dir " + in.Layout.Dir + "`"
	switch {
	case !running && len(others) > 0:
		c.fail(runningCheck, "this install is not running (%s), yet %s; its health is not this install's. Stop that factory before you %s, since the ports 7460-7469 are fixed",
			why, strings.Join(others, "; "), startCmd)
		return answering{skip: "this install is not running; another factory answers on its ports"}
	case !running:
		c.fail(runningCheck, "the factory is not running (%s): %s", why, startCmd)
		return answering{skip: "the factory is not running"}
	case len(others) > 0:
		c.fail(runningCheck, "this install runs, but %s; two factories cannot share the ports 7460-7469: stop the other one and restart this one with `sisf restart --dir %s`",
			strings.Join(others, "; "), in.Layout.Dir)
		return answering{skip: "another factory answers on this install's ports"}
	case m.Mode == manifest.ModeStandalone && (!control.reached || !sup.reached || sup.keyID == ""):
		// The process of this install runs but a unit does not answer, or
		// the supervisor did not name its key: the live checks below say
		// which part is down.
		c.warn(runningCheck, "the `sisf up` process of this install runs, but not every unit answered with this install's identity; see the lines below")
		return answering{ours: true}
	}
	c.ok(runningCheck, "factory %s of this install answers on its ports (%s)", m.FactoryID, why)
	return answering{ours: true}
}

// instanceRunning reports whether a factory of this very install runs: the
// foreground `sisf up` named in its pid file (Standalone) or containers of
// its Compose project (Docker). why says what was found, for the report.
func (in *Installer) instanceRunning(ctx context.Context, m *manifest.Manifest) (bool, string) {
	if m.Mode == manifest.ModeStandalone {
		pid, err := in.runningPID()
		switch {
		case err != nil:
			return false, "its pid file cannot be read"
		case pid == 0:
			return false, "no `sisf up` process of this install"
		}
		return true, "process " + sprintf("%d", pid)
	}
	out, err := in.Exec.Run(ctx, Cmd{Name: "docker", Args: in.composeArgs(m, "ps", "--format", "json")})
	if err != nil {
		if isMissingProgram(err) {
			return false, "docker is not on PATH"
		}
		return false, "Docker Compose does not answer for project " + project(m.FactoryID)
	}
	services, err := parseComposePS(out)
	if err != nil || len(services) == 0 {
		return false, "project " + project(m.FactoryID) + " has no containers"
	}
	return true, "Compose project " + project(m.FactoryID)
}

// controlIdentity asks control's open health route which factory it is and
// which install's root key it names.
func (in *Installer) controlIdentity(ctx context.Context) answeredBy {
	a := answeredBy{port: "port " + sprintf("%d", PortControl)}
	_, body, err := in.httpGet(ctx, hostURL(PortControl, "/healthz"), nil)
	if err != nil {
		return a
	}
	a.reached = true
	var health struct {
		FactoryID string `json:"factory_id"`
		RootKeyID string `json:"root_key_id"`
	}
	if json.Unmarshal(body, &health) == nil {
		a.factoryID, a.keyID = health.FactoryID, health.RootKeyID
	}
	return a
}

// supervisorIdentity asks the supervisor which factory it serves and which
// root key signed the release it verified.
func (in *Installer) supervisorIdentity(ctx context.Context, m *manifest.Manifest) answeredBy {
	a := answeredBy{port: "the supervisor admin address " + addr(PortSupervisor)}
	if m.Mode == manifest.ModeDocker {
		a.port = "the supervisor of project " + project(m.FactoryID)
	}
	status, err := in.supervisorStatus(ctx, m)
	if err != nil {
		return a
	}
	a.reached, a.factoryID, a.keyID = true, string(status.FactoryID), status.SelfCheck.ManifestKeyID
	return a
}

// rootKeyID is the key id of this install's root public key, or empty when
// it cannot be read; the manifest check reports that separately.
func (in *Installer) rootKeyID() string {
	id, _ := in.Layout.RootKeyID() // reported by the manifest check
	return id
}

// RootKeyID is the key id of the install's root public key (signing.KeyID):
// the identifier the doctor, the emergency stop and the CLI compare with
// what a running factory names. It is public, never key material.
func (l Layout) RootKeyID() (string, error) {
	path := l.Secret(areaControl, "root-public-key")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", unavailablef(err, "cannot read the root public key %s", path)
	}
	pub, err := signing.ParsePublicKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return "", invalidf("%s does not hold a root public key", path)
	}
	return signing.KeyID(pub), nil
}

// isMissingProgram reports whether err says a program is not installed.
func isMissingProgram(err error) bool { return errors.Is(err, exec.ErrNotFound) }
