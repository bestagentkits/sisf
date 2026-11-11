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
  - no two zones share credentials or a store role and no secret reference
    stands for two secrets; Zone 1 and Zone 2 mount only their own volumes,
    no unit writes a volume of another zone, a shared volume is mounted
    only by the unit holding it (state, artifacts or edge), and in
    standalone mode no two owners share a directory;
  - the state store and the artifact store are kept on the volumes their
    own units hold;
  - every unit stays on the network segments its role allows, the sandbox
    is internal and leaves through a declared egress proxy, a runner
    listens on no port, and only app, edge and named routes of control are
    public;
  - QA is independent of the coding agent: another provider or model
    (or model_routes.same_model_route: true, at reduced assurance), a
    workspace of its own and credentials of its own;
  - the trusted release supervisor runs from its own pinned artifact, keeps
    the last known good release and the signing keys on a volume only it
    writes and no candidate mounts, has a digest only a governance cycle
    can change, and checks itself at startup;
  - the selected agent, execution and deployment backends are in the
    capability matrix, run in the chosen mode and can do what the manifest
    asks of them; a backend that cannot sandbox agent runs is accepted only
    with trusted_code_only: true, at reduced assurance;
  - secrets are referenced (env:NAME or local:path/name), never written;
  - release.units lists only app, control, dispatcher and runner, a
    synthetic suite backs self-publishing, rollback thresholds can be
    reached, a canary ends at 100, and an unlimited budget states its
    alert threshold or that none is wanted;
  - release.unit_inputs, when set, names only released units, and none of
    its patterns can match a protected path, Go code of the control unit
    or an input of another unit.

Each finding names the rule that failed, the place in the manifest and the
change that fixes it, and never repeats a value that may be a secret. Any
finding exits 1. A conformant manifest is reported with every reduction of
assurance and every accepted risk, such as an egress policy that blocks
nothing.`,
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
	// The verdict never stands alone: reduced assurance and accepted risks
	// are printed beside it.
	var notes strings.Builder
	for _, note := range report.Notes() {
		notes.WriteString("  " + note + "\n")
	}
	return env.Printf("%s: conformant\n%s", path, notes.String())
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
