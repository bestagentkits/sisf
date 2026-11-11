// Command gentrustedpackages writes the list of the Go package directories
// the trusted binaries link into the release package. It runs through
// go generate ./internal/supervisor/release/.
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
	flag.Parse()
	if err := run(*root, *out, *pkg); err != nil {
		fmt.Fprintln(os.Stderr, "gentrustedpackages:", err)
		os.Exit(1)
	}
}

func run(root, out, pkg string) error {
	if out == "" || pkg == "" {
		return fmt.Errorf("-out and -package are required")
	}
	packages, err := trustedclosure.Packages(root)
	if err != nil {
		return err
	}
	dirs, err := trustedclosure.Dirs(packages)
	if err != nil {
		return err
	}
	src, err := trustedclosure.Render(pkg, dirs)
	if err != nil {
		return err
	}
	return os.WriteFile(out, src, 0o644)
}
