package main

import (
	"context"
	"io"
	"strings"

	"github.com/bestagentkits/sisf/internal/conformance"
)

// defaultManifest is the manifest checked when no file is named.
const defaultManifest = ".sisf/factory.yaml"

func init() {
	register(Command{
		Path:    []string{"conformance", "check"},
		Args:    "[file]",
		Summary: "Check that a factory manifest declares one conformant deployment",
		Help: `
Checks the manifest (default ` + defaultManifest + `) and exits 0 only when it is conformant:

  - all three zones, all nine runtime units and all four network segments
    are declared under one factory_id, and every unit that runs pins its
    digest;
  - factory_id, deployment_id and the runtime units can be told apart;
  - no two zones share credentials or a store role; Zone 1 and Zone 2 mount
    only their own volumes, no unit writes a volume of another zone, and a
    shared volume is mounted only by the state or artifacts unit holding it;
  - every unit stays on the network segments its role allows, the sandbox
    is internal and leaves through a declared egress proxy, and only app,
    edge and named routes of control are public;
  - the trusted release supervisor runs from its own pinned artifact, keeps
    the last known good release and the signing keys on a volume only it
    writes and no candidate mounts, has a digest only a governance cycle
    can change, and checks itself at startup;
  - the selected agent, execution and deployment backends are in the
    capability matrix, run in the chosen mode and can do what the manifest
    asks of them; a backend that cannot sandbox agent runs is accepted only
    with trusted_code_only: true, at reduced assurance;
  - secrets are referenced (env:NAME or local:path/name), never written;
  - release windows, canary steps and model routes are not left empty.

Each finding names the rule that failed, the place in the manifest and the
change that fixes it, and never repeats a value that may be a secret. Any
finding exits 1.`,
		Run: runConformanceCheck,
	})
}

// runConformanceCheck checks one manifest and reports the findings.
func runConformanceCheck(_ context.Context, env *Env, args []string) error {
	path := defaultManifest
	switch {
	case len(args) > 1:
		return usagef("expected one manifest file, got %d arguments", len(args))
	case len(args) == 1 && strings.HasPrefix(args[0], "-"):
		return usagef("unknown flag %s", args[0])
	case len(args) == 1:
		path = args[0]
	}
	report, err := conformance.CheckFile(path)
	if err != nil {
		return err
	}
	if err := report.Err(); err != nil {
		printFindings(env.Stderr, report)
		return err
	}
	selfUpdate := "enabled, the supervisor boundary holds"
	if !report.SelfUpdate {
		selfUpdate = "disabled, no trusted supervisor is declared"
	}
	assurance := ""
	if report.TrustedCodeOnly {
		assurance = "  assurance: reduced, trusted code only: the execution backend does not isolate agent runs or enforce the egress policy\n"
	}
	return env.Printf("%s: conformant\n  self-update: %s\n%s", path, selfUpdate, assurance)
}

// printFindings lists what is wrong with the manifest, one block per finding.
func printFindings(w io.Writer, report conformance.Report) {
	say(w, "%s: not conformant\n", report.Source)
	for _, finding := range report.Findings {
		at := finding.Path
		if at == "" {
			at = "(whole manifest)"
		}
		say(w, "\n  [%s] %s\n      %s\n", finding.Rule, at, finding.Message)
		if finding.Fix != "" {
			say(w, "      fix: %s\n", finding.Fix)
		}
	}
	say(w, "\n")
}
