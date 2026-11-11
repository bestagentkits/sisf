package main

import (
	"context"
	"flag"
	"runtime"
	"runtime/debug"
)

// Set at build time, for example:
//
//	go build -ldflags "-X main.version=v0.3.0 -X main.commit=$(git rev-parse HEAD) \
//	  -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" ./cmd/sisf
//
// Without them the version comes from the build information Go records:
// the module version for `go install ...@version`, and the commit and its
// time for a build from a checkout.
var (
	version   = ""
	commit    = ""
	buildDate = ""
)

// versionInfo describes this build of sisf.
type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	// Modified is true when the checkout had uncommitted changes.
	Modified  bool   `json:"modified,omitempty"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// buildVersion returns the version of this build, from the link-time values
// first and the recorded build information second; "unknown" fills what
// neither knows.
func buildVersion() versionInfo {
	v := versionInfo{Version: version, Commit: commit, BuildDate: buildDate,
		GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v.Version == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v.Version = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if v.Commit == "" {
					v.Commit = s.Value
				}
			case "vcs.time":
				if v.BuildDate == "" {
					v.BuildDate = s.Value
				}
			case "vcs.modified":
				v.Modified = s.Value == "true"
			}
		}
	}
	if v.Version == "" {
		v.Version = "devel"
		if len(v.Commit) >= 12 {
			v.Version += "-" + v.Commit[:12]
		}
	}
	for _, f := range []*string{&v.Commit, &v.BuildDate} {
		if *f == "" {
			*f = "unknown"
		}
	}
	return v
}

// versionLine is the one-line form of the version.
func versionLine() string {
	v := buildVersion()
	line := program + " " + v.Version + " (commit " + v.Commit
	if v.Modified {
		line += ", modified"
	}
	return line + ", built " + v.BuildDate + ", " + v.GoVersion + " " + v.Platform + ")"
}

func init() {
	register(Command{
		Path:    []string{"version"},
		Args:    "[--json]",
		Summary: "Print the version, commit and build date of this sisf",
		Help: `
Prints the version of this sisf, the commit it was built from, the build
date, the Go version and the platform; name them in a bug report.
sisf --version prints the same line. --json prints one object with the
fields version, commit, build_date, modified, go_version and platform.`,
		Flags: bindVersionFlags(new(bool)),
		Run: func(_ context.Context, env *Env, args []string) error {
			var asJSON bool
			fs := newFlagSet("version")
			bindVersionFlags(&asJSON)(fs)
			if err := parseFlags(fs, args); err != nil {
				return err
			}
			if asJSON {
				return printJSON(env, buildVersion())
			}
			return env.Printf("%s\n", versionLine())
		},
	})
}

// bindVersionFlags registers the version command's flag into asJSON.
func bindVersionFlags(asJSON *bool) func(*flag.FlagSet) {
	return func(fs *flag.FlagSet) {
		fs.BoolVar(asJSON, "json", false, "print one JSON object")
	}
}
