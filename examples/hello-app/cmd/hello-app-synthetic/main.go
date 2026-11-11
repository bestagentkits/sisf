// Command hello-app-synthetic runs the sample app's synthetic suite once
// against one instance. A factory's supervisor runs it during the
// low-traffic hold of a release, when real requests are too few to judge
// the release by.
//
// Usage:
//
//	hello-app-synthetic --url http://127.0.0.1:7461 --revision sha256:…
//
// It prints one line per check. Exit status: 0 when every check passed, 1
// when one failed, 2 on a usage error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/bestagentkits/sisf/examples/hello-app/synthetic"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run runs the suite once and returns the exit status.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hello-app-synthetic", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "base URL of the instance under test")
	revision := fs.String("revision", "", "revision the instance must report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *url == "" || *revision == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "hello-app-synthetic: --url and --revision are required, and nothing else")
		return 2
	}
	report := synthetic.Run(ctx, synthetic.Target{BaseURL: *url, ExpectedRevision: *revision})
	for _, r := range report.Results {
		if r.Err != nil {
			_, _ = fmt.Fprintf(stdout, "FAIL %s: %v\n", r.Name, r.Err)
		} else {
			_, _ = fmt.Fprintf(stdout, "ok   %s\n", r.Name)
		}
	}
	if !report.Passed() {
		return 1
	}
	return 0
}
