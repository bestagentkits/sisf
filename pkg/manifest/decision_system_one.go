package manifest

import (
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// System One backends a manifest may name.
const (
	SystemOneJev  = "jev"
	SystemOneClef = "clef"
)

// SystemOneMaxTimeout bounds how long one System One judgment may take.
const SystemOneMaxTimeout = 2 * time.Minute

// systemOneVersionPattern accepts an explicit protocol version such as "1",
// "v2" or "2026-11-01", never a floating name such as "latest".
const systemOneVersionPattern = `^v?[0-9][A-Za-z0-9._-]{0,63}$`

// Decision configures how the factory decides what to work on. Left out, or
// without a System One backend, the deterministic rules decide alone.
type Decision struct {
	SystemOne *SystemOne `json:"system_one,omitempty" yaml:"system_one,omitempty"`
}

// SystemOneConfig returns the declared System One backend and whether one is
// declared. It is safe to call on a nil Decision.
func (d *Decision) SystemOneConfig() (*SystemOne, bool) {
	if d == nil || d.SystemOne == nil {
		return nil, false
	}
	return d.SystemOne, true
}

// SystemOne is the one System One decision model the factory consults. Its
// keys are those of the System One adapter configuration, so the two convert
// field by field.
type SystemOne struct {
	// Backend is SystemOneJev or SystemOneClef.
	Backend  string `json:"backend" yaml:"backend"`
	Endpoint string `json:"endpoint" yaml:"endpoint"`
	// Version is the protocol version the backend must answer with.
	Version string   `json:"version" yaml:"version"`
	Timeout Duration `json:"timeout" yaml:"timeout"`
	// Credential is a secret reference; empty means the backend takes none.
	Credential string `json:"credential,omitempty" yaml:"credential,omitempty"`
	// Model is the route a judgment must report; any other is refused.
	Model core.ModelRoute `json:"model" yaml:"model"`
}

// systemOnePath is where the section sits in the manifest.
const systemOnePath = "decision.system_one"

// decisionViolations checks what the schema cannot say about the System One
// section: the timeout bound, the endpoint's form and a model route that is
// only white space. The backend, version and credential forms are schema
// rules. Messages never repeat a refused value.
func decisionViolations(m *Manifest) []Violation {
	s, ok := m.Decision.SystemOneConfig()
	if !ok {
		return nil
	}
	var found []Violation
	if s.Timeout <= 0 || s.Timeout.Std() > SystemOneMaxTimeout {
		found = append(found, Violation{Path: systemOnePath + ".timeout",
			Message: "must be above zero and at most " + SystemOneMaxTimeout.String()})
	}
	if message := endpointProblem(s.Endpoint); message != "" {
		found = append(found, Violation{Path: systemOnePath + ".endpoint", Message: message + " (the value is not shown)"})
	}
	if s.Model.Validate() != nil {
		found = append(found, Violation{Path: systemOnePath + ".model", Message: "needs a provider and a model"})
	}
	return found
}

// endpointProblem says what is wrong with a System One endpoint, or returns
// "" when it is an absolute URL with no user information, query or fragment,
// so a credential cannot hide in it, served over HTTPS or over plain HTTP on a
// loopback address.
func endpointProblem(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "must be an absolute URL"
	}
	switch {
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return "must not carry user information, a query or a fragment"
	case u.Scheme == "https":
		return ""
	case u.Scheme == "http" && loopbackHost(u.Hostname()):
		return ""
	}
	return "must use HTTPS, or HTTP on a loopback address"
}

// loopbackHost reports whether host is localhost or a loopback address.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
