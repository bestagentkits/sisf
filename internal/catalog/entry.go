// Package catalog writes what a verified release changed into the
// repository's changelog and approved feature catalog. It never writes to a
// branch someone reviews from: every change goes through a pull request
// opened as the Publisher identity, and the Owner merges it.
//
// A release can mark an approved feature as built. It cannot add a feature:
// a feature the catalog does not list needs the Owner's approval first.
package catalog

import (
	"strings"
	"time"
	"unicode"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Paths of the files the catalog writes, relative to the repository root.
const (
	PathFeatures     = "FEATURES.md"
	PathFeaturesYAML = "features.yaml"
	PathChangelog    = "CHANGELOG.md"
)

// DefaultStatus is the catalog status of a feature a verified release built.
const DefaultStatus = "built and released"

// maxTextLength bounds a summary, a feature name and a status.
const maxTextLength = 200

// Entry is one verified release to record.
type Entry struct {
	IssueID core.IssueRef      `json:"issue_id"`
	RunID   core.RunID         `json:"run_id"`
	Unit    string             `json:"unit"`
	Release core.ReleaseDigest `json:"release"`
	// Feature is the approved catalog entry the release built; empty when
	// the release changes no catalog entry.
	Feature string `json:"feature,omitempty"`
	// Status is the catalog status to set; DefaultStatus when empty.
	Status string `json:"status,omitempty"`
	// Summary is the changelog line. It is data: it is reduced to one line
	// of plain text before it is written.
	Summary    string    `json:"summary,omitempty"`
	VerifiedAt time.Time `json:"verified_at"`
}

// Validate requires the release to be named by its issue, run, unit and
// digest, and verified at a known instant.
func (e Entry) Validate() error {
	if err := e.IssueID.Validate(); err != nil {
		return err
	}
	if err := e.RunID.Validate(); err != nil {
		return err
	}
	if err := e.Release.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(e.Unit) == "" || e.VerifiedAt.IsZero() {
		return core.NewError(core.CodeInvalidArgument, "a catalog entry needs the unit and the instant the release was verified")
	}
	return nil
}

// status returns the status to write.
func (e Entry) status() string {
	if s := plain(e.Status); s != "" {
		return s
	}
	return DefaultStatus
}

// plain reduces untrusted text to one bounded line with no control
// characters, no table separator and no HTML comment, so it cannot break
// the file it is written into or forge a marker.
func plain(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.NewReplacer("|", "/", "<!--", "", "-->", "").Replace(text)
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxTextLength {
		text = strings.TrimSpace(text[:maxTextLength])
	}
	return text
}

// featureKey is the form in which feature names are compared: case and
// spacing do not matter, as in the policy loader.
func featureKey(name string) string { return strings.ToLower(strings.Join(strings.Fields(name), " ")) }
