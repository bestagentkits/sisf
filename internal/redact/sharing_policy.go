package redact

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// DataClass is a class of data that might be sent to a remote agent or a
// model provider.
type DataClass string

const (
	ClassSourceCode  DataClass = "source_code"
	ClassIssueText   DataClass = "issue_text"
	ClassLogs        DataClass = "logs"
	ClassTranscripts DataClass = "transcripts"
	ClassTelemetry   DataClass = "telemetry"
	// ClassPersonalData is personal data left in the other classes. Without
	// it, personal data is removed from whatever is sent.
	ClassPersonalData DataClass = "personal_data"
	// ClassSecrets can be named but never allowed.
	ClassSecrets DataClass = "secrets"
)

var allClasses = []DataClass{ClassSourceCode, ClassIssueText, ClassLogs, ClassTranscripts,
	ClassTelemetry, ClassPersonalData, ClassSecrets}

// Valid reports whether c is a known class.
func (c DataClass) Valid() bool { return slices.Contains(allClasses, c) }

// SharingPolicy states which classes of data each provider may receive. A
// provider it does not name receives nothing.
type SharingPolicy struct {
	// Providers maps a provider name, compared without regard to case or
	// surrounding space, to the classes it may receive.
	Providers map[string][]DataClass `json:"providers" yaml:"providers"`
}

// DefaultSharingPolicy lets the named providers receive what a coding agent
// needs (code, issue text, redacted logs and transcripts) and nothing else.
func DefaultSharingPolicy(providers ...string) SharingPolicy {
	p := SharingPolicy{Providers: map[string][]DataClass{}}
	for _, name := range providers {
		p.Providers[name] = []DataClass{ClassSourceCode, ClassIssueText, ClassLogs, ClassTranscripts}
	}
	return p
}

// Validate rejects blank or duplicate provider names, unknown classes and
// any policy that would let secrets leave.
func (p SharingPolicy) Validate() error {
	seen := map[string]bool{}
	for name, classes := range p.Providers {
		key := providerKey(name)
		if key == "" || seen[key] {
			return core.NewError(core.CodeInvalidArgument, "sharing policy needs distinct, non-blank provider names")
		}
		seen[key] = true
		for _, c := range classes {
			if !c.Valid() {
				return core.NewError(core.CodeInvalidArgument, fmt.Sprintf("sharing policy names an unknown data class %q", c))
			}
			if c == ClassSecrets {
				return core.NewError(core.CodeInvalidArgument, "secrets are never shared with a provider")
			}
		}
	}
	return nil
}

// Allows returns nil when provider may receive data of class. An invalid
// policy allows nothing.
func (p SharingPolicy) Allows(provider string, class DataClass) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if !class.Valid() {
		return core.NewError(core.CodeInvalidArgument, fmt.Sprintf("unknown data class %q", class))
	}
	if class != ClassSecrets && slices.Contains(p.classes(provider), class) {
		return nil
	}
	return core.NewError(core.CodePermissionDenied,
		fmt.Sprintf("the sharing policy does not let provider %q receive %s", strings.TrimSpace(provider), class))
}

// Prepare checks the policy and returns text as it may be sent: secrets are
// always removed, and personal data too unless the provider may receive it.
func (p SharingPolicy) Prepare(r *Redactor, provider string, class DataClass, text string) (string, error) {
	if r == nil {
		return "", core.NewError(core.CodeInvalidArgument, "preparing data for a provider needs a redactor")
	}
	if err := p.Allows(provider, class); err != nil {
		return "", err
	}
	if p.Allows(provider, ClassPersonalData) == nil {
		return r.Secrets(text), nil
	}
	return r.Redact(text), nil
}

func (p SharingPolicy) classes(provider string) []DataClass {
	key := providerKey(provider)
	for name, classes := range p.Providers {
		if providerKey(name) == key {
			return classes
		}
	}
	return nil
}

func providerKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }
