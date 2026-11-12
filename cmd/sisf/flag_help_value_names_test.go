package main

import (
	"flag"
	"strings"
	"testing"
)

// The flag package takes a back-quoted word of a flag's help for the name
// of its value, so `gh auth token` would render as --flag gh auth token.
// Every value name a command's help shows is one word.
func TestNoFlagHelpTurnsABackQuotedPhraseIntoItsValueName(t *testing.T) {
	t.Parallel()
	for _, c := range commands.commands {
		if c.Flags == nil {
			continue
		}
		helpFlags(c).VisitAll(func(f *flag.Flag) {
			if kind, _ := flag.UnquoteUsage(f); strings.ContainsAny(kind, " \t") {
				t.Errorf("%s --%s shows %q as the name of its value", c.name(), f.Name, kind)
			}
		})
	}
}

// docker-filter serves a Docker install's Compose project and, started by
// the operator, the build sandbox of a Standalone install; its help says
// both and forbids neither.
func TestDockerFilterHelpCoversBothOfItsUses(t *testing.T) {
	t.Parallel()
	_, stdout, _ := runWithOutput(t, "help", "docker-filter")
	for _, want := range []string{"Docker install", "Standalone", "build sandbox", "--exec-docker-endpoint"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("docker-filter help does not mention %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "never by hand") {
		t.Errorf("docker-filter help says it is never started by hand:\n%s", stdout)
	}
}
