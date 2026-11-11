// Command gentrustedpackages writes a generated list of Go package
// directories: with -list trusted, the directories the trusted binaries
// link (go generate ./internal/supervisor/release/); with -list control,
// the directories only the control unit links (go generate
// ./cmd/supervisor/stages/). See package trustedclosure.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/bestagentkits/sisf/internal/supervisor/trustedclosure"
)

func main() {
	root := flag.String("root", ".", "the module's root directory")
	out := flag.String("out", "", "the Go file to write")
	pkg := flag.String("package", "", "the package of the Go file")
	list := flag.String("list", "trusted", "the list to write: trusted or control")
	flag.Parse()
	if err := run(*root, *out, *pkg, *list); err != nil {
		fmt.Fprintln(os.Stderr, "gentrustedpackages:", err)
		os.Exit(1)
	}
}

func run(root, out, pkg, list string) error {
	if out == "" || pkg == "" {
		return fmt.Errorf("-out and -package are required")
	}
	var (
		packages []string
		err      error
		of       trustedclosure.List
	)
	switch list {
	case "trusted":
		packages, err = trustedclosure.Packages(root)
		of = trustedclosure.TrustedList
	case "control":
		packages, err = trustedclosure.ControlOnlyPackages(root)
		of = trustedclosure.ControlList
	default:
		return fmt.Errorf("-list must be trusted or control, not %q", list)
	}
	if err != nil {
		return err
	}
	dirs, err := trustedclosure.Dirs(packages)
	if err != nil {
		return err
	}
	src, err := trustedclosure.Render(pkg, of, dirs)
	if err != nil {
		return err
	}
	return os.WriteFile(out, src, 0o644)
}
