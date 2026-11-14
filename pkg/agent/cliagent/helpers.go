package cliagent

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// modelPattern is what a model name may look like. It cannot start with a
// dash, so a route can never be read by the tool as an option.
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,127}$`)

// ValidateModel rejects a model name that is empty, too long or could be
// mistaken for a command-line option.
func ValidateModel(model string) error {
	if !modelPattern.MatchString(model) {
		return core.NewError(core.CodeInvalidArgument, "the model name is empty, too long or has characters a model name cannot have")
	}
	return nil
}

// ValidateEffort accepts an empty effort, which leaves the tool's default,
// or one of allowed.
func ValidateEffort(effort string, allowed ...string) error {
	if effort == "" || slices.Contains(allowed, effort) {
		return nil
	}
	return core.NewError(core.CodeInvalidArgument, "the effort level "+Clip(effort, 32)+" is not one this tool accepts; use one of "+strings.Join(allowed, ", "))
}

// RequireProvider checks that a route names the provider a tool serves.
func RequireProvider(route core.ModelRoute, provider, tool string) error {
	if !strings.EqualFold(strings.TrimSpace(route.Provider), provider) {
		return core.NewError(core.CodeNotSupported, "the "+tool+" backend serves only "+provider+" routes")
	}
	return nil
}

// Micros converts a dollar amount to millionths, rounding to the nearest,
// within what the ledger books: 0 for a negative or meaningless amount and
// at most core.MaxUsageCost. The bound is applied before the conversion to
// an integer, whose result for a float out of range Go leaves to the
// platform.
func Micros(usd float64) core.Micros {
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0
	}
	micros := math.Round(usd * 1e6)
	if micros >= float64(core.MaxUsageCost) {
		return core.MaxUsageCost
	}
	return core.Micros(micros)
}

// Text returns the text of a JSON value that is a string, or the text blocks
// of an array of content blocks, or else the value's own JSON.
func Text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}
