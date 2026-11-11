// Package trustedclosure computes, from what the binaries of this module
// link, two lists of repository directories:
//   - The directories the trusted binaries link: the dependency closure of
//     the release supervisor, which also runs the QA evaluator. Every one
//     of them is part of the trusted control surface, so the release
//     pipeline refuses a candidate that changes any of them as a governance
//     change. The list is generated into the release package (go generate
//     ./internal/supervisor/release/).
//   - The directories of the packages the application links: its inputs
//     among the Go packages of this module. A candidate the factory
//     publishes by itself as a release of the application may change only
//     files directly in those directories (and the application's
//     Dockerfile). The list is generated into the release package as well
//     (go generate ./internal/supervisor/release/).
//
// A test beside each generated list fails when that list is stale.
package trustedclosure

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// Module is the import path of this module.
const Module = "github.com/bestagentkits/sisf"

// TrustedBinaries are the main packages whose closure is trusted. The QA
// evaluator is part of the supervisor, not a binary of its own.
var TrustedBinaries = []string{"./cmd/supervisor"}

// targets are the platforms the supervisor is built for: the Compose image
// on Linux and the Standalone host on Linux or macOS, with and without cgo.
// The closure is their union, so a file selected by a build constraint on
// one platform only is still covered.
var targets = []struct{ goos, goarch, cgo string }{
	{"linux", "amd64", "0"}, {"linux", "arm64", "0"}, {"darwin", "amd64", "0"}, {"darwin", "arm64", "0"},
	{"linux", "amd64", "1"}, {"linux", "arm64", "1"}, {"darwin", "amd64", "1"}, {"darwin", "arm64", "1"},
}

// ApplicationBinaries are the main packages of the application the factory
// builds and observes. Its synthetic suite and the suite's command are not
// part of it: they judge the application and are trusted.
var ApplicationBinaries = []string{"./examples/hello-app"}

// Packages returns the import paths of this module's packages that the
// trusted binaries link on any target, sorted and without repeats. root is
// the module's root directory.
func Packages(root string) ([]string, error) { return Closure(root, TrustedBinaries...) }

// ApplicationPackages returns the import paths of this module's packages
// that the application links on any target, itself included.
func ApplicationPackages(root string) ([]string, error) { return Closure(root, ApplicationBinaries...) }

// Closure returns the import paths of this module's packages that the
// packages matched by patterns link on any target, sorted and without
// repeats. root is the module's root directory.
func Closure(root string, patterns ...string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("no package to list the dependencies of")
	}
	var out []string
	for _, t := range targets {
		args := append([]string{"list", "-deps", "-f", "{{if .Module}}{{if .Module.Main}}{{.ImportPath}}{{end}}{{end}}"}, patterns...)
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS="+t.goos, "GOARCH="+t.goarch, "CGO_ENABLED="+t.cgo, "GOFLAGS=")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		raw, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list for %s/%s cgo=%s: %w: %s", t.goos, t.goarch, t.cgo, err, strings.TrimSpace(stderr.String()))
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				out = append(out, line)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Dirs turns import paths of this module into repository directories, each
// ending in "/" so it covers the directory and everything below it.
func Dirs(packages []string) ([]string, error) {
	out := make([]string, 0, len(packages))
	for _, p := range packages {
		rel, ok := strings.CutPrefix(p, Module+"/")
		if !ok || rel == "" {
			return nil, fmt.Errorf("package %q is not inside module %s", p, Module)
		}
		out = append(out, rel+"/")
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// List is a generated list: the variable it declares and the sentences
// that document it.
type List struct {
	Var, Doc string
}

// TrustedList is the list of the directories the trusted binaries link.
var TrustedList = List{
	Var: "trustedGoPackageDirs",
	Doc: "are the directories of the Go packages the trusted binaries link (" + strings.Join(TrustedBinaries, ", ") +
		"), on every platform they are built for.",
}

// AppList is the list of the directories of the packages the application
// links.
var AppList = List{
	Var: "appUnitPackageDirs",
	Doc: "are the directories of the Go packages the application (" + strings.Join(ApplicationBinaries, ", ") +
		") links, itself included, on every platform. Each covers the files directly in it, not its subdirectories.",
}

// Render is the Go source of list, holding dirs, in package pkg, gofmt-ed.
func Render(pkg string, list List, dirs []string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("// Code generated by gentrustedpackages; DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	line := "//"
	for _, word := range strings.Fields(list.Var + " " + list.Doc + " See package trustedclosure.") {
		if len(line)+1+len(word) > 76 {
			b.WriteString(line + "\n")
			line = "//"
		}
		line += " " + word
	}
	b.WriteString(line + "\n")
	fmt.Fprintf(&b, "var %s = []string{\n", list.Var)
	for _, d := range dirs {
		fmt.Fprintf(&b, "\t%q,\n", d)
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}
