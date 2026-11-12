// Package redact removes secrets and personal data from text before it
// reaches logs, agent transcripts, issues or pull requests, keeps each kind
// of stored data only for its retention period, and states which data may
// be sent to a remote agent or model provider.
//
// Credential shapes and known secret values are recognised by the agent
// redactor; this package adds personal data on top of it, so both are
// removed by one call.
package redact

import (
	"maps"

	agentredact "github.com/bestagentkits/sisf/pkg/agent/redact"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// Marker replaces a redacted secret.
const Marker = agentredact.Marker

// Markers that replace each kind of personal data, so a reader can tell
// what was removed without seeing it.
const (
	MarkerEmail = "[REDACTED:email]"
	MarkerPhone = "[REDACTED:phone]"
	MarkerIP    = "[REDACTED:ip]"
	MarkerCard  = "[REDACTED:card]"
)

// Config lists the secrets the process holds, so their exact values are
// redacted even when they have no recognisable shape.
type Config struct {
	// Env holds KEY=VALUE entries; values whose name looks like a
	// credential are treated as secrets.
	Env []string
	// Secrets are further exact values to redact.
	Secrets []string
}

// Redactor removes secrets and personal data. It is safe for concurrent use.
type Redactor struct {
	secrets *agentredact.Redactor
}

var _ telemetry.Redactor = (*Redactor)(nil)

// New builds a redactor for the secrets in cfg.
func New(cfg Config) *Redactor {
	return &Redactor{secrets: agentredact.New(cfg.Env, cfg.Secrets...)}
}

// Redact returns text without secrets or personal data.
func (r *Redactor) Redact(text string) string { return personalData(r.Secrets(text)) }

// Secrets returns text without secrets, leaving personal data in place. It
// is for destinations the Owner has allowed to receive personal data.
func (r *Redactor) Secrets(text string) string {
	if r == nil || r.secrets == nil {
		return agentredact.Text(text)
	}
	return r.secrets.Text(text)
}

// Fields returns a copy of fields with every value redacted. Keys are
// names chosen by code and are kept as they are.
func (r *Redactor) Fields(fields map[string]string) map[string]string {
	out := maps.Clone(fields)
	for k, v := range out {
		out[k] = r.Redact(v)
	}
	return out
}
