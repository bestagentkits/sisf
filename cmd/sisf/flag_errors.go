package main

import (
	"regexp"
	"strings"
)

// The messages of the flag package, which name a flag in Go's -name style.
var (
	flagUndefined    = regexp.MustCompile(`^flag provided but not defined: -+(.+)$`)
	flagNeedsValue   = regexp.MustCompile(`^flag needs an argument: -+(.+)$`)
	flagInvalidValue = regexp.MustCompile(`^invalid (?:boolean )?value (".*") for (?:flag )?-+([^:]+): (.*)$`)
)

// flagMessage rewrites an error of the flag package in the CLI's own words:
// flags are written --name, and a value that does not parse is named with
// the flag it was given to.
func flagMessage(err error) string {
	text := err.Error()
	if m := flagUndefined.FindStringSubmatch(text); m != nil {
		return "unknown flag --" + m[1]
	}
	if m := flagNeedsValue.FindStringSubmatch(text); m != nil {
		return "--" + m[1] + " needs a value"
	}
	if m := flagInvalidValue.FindStringSubmatch(text); m != nil {
		reason := strings.TrimSpace(m[3])
		if reason == "parse error" || strings.HasPrefix(reason, "strconv.") {
			return "--" + m[2] + ": " + m[1] + " is not a valid value"
		}
		return "--" + m[2] + ": " + m[1] + ": " + reason
	}
	return text
}
