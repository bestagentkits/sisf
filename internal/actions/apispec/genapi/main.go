// Command genapi writes api/openapi.yaml and api/llms.txt from the action
// registry. It runs through go generate ./internal/actions/...
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/bestagentkits/sisf/internal/actions/apispec"
)

func main() {
	dir := flag.String("dir", "api", "directory the documents are written to")
	flag.Parse()
	if err := apispec.Generate(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "genapi:", err)
		os.Exit(1)
	}
}
