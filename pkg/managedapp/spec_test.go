package managedapp

import (
	"errors"
	"strings"
	"testing"
)

const digest = "sha256:b427cec767f5de2aa649390cb8805aa1fe320e1e0d57fc1f467754edb6cc0a49"

func validSpec() *Spec {
	return &Spec{
		APIVersion: APIVersion, Name: "shop", Stack: "wordpress",
		Runtime: Runtime{
			Image: "wordpress:7.0-php8.3-apache@" + digest, Port: 80, User: "33:33", HealthPath: "/healthz",
			Env:     map[string]string{"APACHE_RUN_DIR": "/tmp/apache2"},
			Secrets: map[string]string{"WORDPRESS_DB_PASSWORD": "db_password"},
		},
		Bundle: Bundle{Lock: "content.lock", Mount: "/var/www/html", Sources: []Source{{From: "plugins", To: "wp-content/plugins"}}},
		Data:   []Volume{{Name: "uploads", Target: "/var/www/html/wp-content/uploads", Owner: "33:33"}},
		Dependencies: []Dependency{{
			Name: "db", Image: "mariadb:11.4@" + digest, Secrets: map[string]string{"MARIADB_PASSWORD": "db_password"},
			Data: []Volume{{Name: "db", Target: "/var/lib/mysql"}}, Backup: []string{"dump"}, Restore: []string{"load"},
		}},
		Checks: []Check{{Name: "home", Steps: []Step{{Path: "/"}}}},
		Policy: Policy{Protected: []string{"plugins/sisf-sandbox-pay/*"}, LockChanges: map[LockChange]Decision{LockPatch: DecisionAuto}},
	}
}

func TestAValidSpecHasNoProblems(t *testing.T) {
	t.Parallel()
	if p := validSpec().Validate(); p != nil {
		t.Fatalf("problems: %v", p)
	}
}

// Every rule that keeps a managed application inside its boundary refuses
// a Spec that breaks it.
func TestASpecThatBreaksABoundaryIsRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate func(*Spec)
		want   string
	}{
		"unpinned runtime":       {func(s *Spec) { s.Runtime.Image = "wordpress:latest" }, "runtime.image"},
		"unpinned dependency":    {func(s *Spec) { s.Dependencies[0].Image = "mariadb:11.4" }, "dependency db image"},
		"root runtime":           {func(s *Spec) { s.Runtime.User = "0:0" }, "runs as root"},
		"secret in env":          {func(s *Spec) { s.Runtime.Env["DB_PASSWORD"] = "x" }, "looks like a secret"},
		"revision set by spec":   {func(s *Spec) { s.Runtime.Env["SISF_REVISION"] = "x" }, "set by the factory"},
		"source escapes repo":    {func(s *Spec) { s.Bundle.Sources[0].From = "../etc" }, "bundle.sources[0].from"},
		"source absolute":        {func(s *Spec) { s.Bundle.Sources[0].To = "/etc" }, "bundle.sources[0].to"},
		"mount at root":          {func(s *Spec) { s.Bundle.Mount = "/" }, "bundle.mount"},
		"volume twice":           {func(s *Spec) { s.Data = append(s.Data, s.Data[0]) }, "declared twice"},
		"backup without restore": {func(s *Spec) { s.Dependencies[0].Restore = nil }, "only one of backup and restore"},
		"no checks":              {func(s *Spec) { s.Checks = nil }, "at least one check"},
		"unknown lock class":     {func(s *Spec) { s.Policy.LockChanges["everything"] = DecisionAuto }, "unknown class"},
		"absolute pattern":       {func(s *Spec) { s.Policy.Protected = []string{"/etc/*"} }, "policy.protected"},
		"two ways to bundle":     {func(s *Spec) { s.Bundle.Program = "./cmd/app" }, "both lock and program"},
		"no way to bundle":       {func(s *Spec) { s.Bundle.Lock = "" }, "neither lock nor program"},
		"wrong api version":      {func(s *Spec) { s.APIVersion = "v0" }, "apiVersion"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := validSpec()
			c.mutate(s)
			p := s.Validate()
			if !strings.Contains(strings.Join(p, "\n"), c.want) {
				t.Fatalf("want a problem naming %q, got %v", c.want, p)
			}
		})
	}
}

// A core change and a new component are the Owner's to release whatever
// the Spec says: a candidate cannot make them automatic.
func TestCoreAndAddedChangesAlwaysNeedTheOwner(t *testing.T) {
	t.Parallel()
	p := Policy{LockChanges: map[LockChange]Decision{LockCore: DecisionAuto, LockAdded: DecisionAuto, LockPatch: DecisionAuto}}
	for _, c := range []LockChange{LockCore, LockAdded, LockMajor, LockMinor, LockRemoved} {
		if p.Decide(c) != DecisionOwner {
			t.Errorf("%s: want owner", c)
		}
	}
	if p.Decide(LockPatch) != DecisionAuto {
		t.Error("patch: want auto as declared")
	}
}

func TestTheSpecAndItsLockAreAlwaysProtected(t *testing.T) {
	t.Parallel()
	s := validSpec()
	s.Policy.Protected = nil
	got := strings.Join(s.ProtectedPaths(), ",")
	if got != "content.lock,sisf-app.yaml" {
		t.Fatalf("protected: %s", got)
	}
}

func TestParseIsStrictAndRoundTrips(t *testing.T) {
	t.Parallel()
	data, err := Marshal(validSpec(), "written by a test")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(data)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if s.Runtime.Image != validSpec().Runtime.Image {
		t.Fatal("image lost in round trip")
	}
	if _, err := Parse(append(data, []byte("surprise: 1\n")...)); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	bad := strings.Replace(string(data), "user: \"33:33\"", "user: \"0:0\"", 1)
	var inv *InvalidError
	if _, err := Parse([]byte(bad)); !errors.As(err, &inv) {
		t.Fatalf("a root runtime parsed: %v", err)
	}
}
