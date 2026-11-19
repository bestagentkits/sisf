package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// The trial shop's own pins are clean; pinning the vulnerable Contact Form 7
// release is reported and fails the command.
func TestAppAdvisoriesReportsTheVulnerablePlugin(t *testing.T) {
	t.Parallel()
	shop := "../../examples/woocommerce-app"
	snapshot := filepath.Join(shop, "advisories.snapshot")
	var out bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &out}
	if err := runAppAdvisories(context.Background(), env, []string{"--spec", filepath.Join(shop, managedapp.FileName), "--snapshot", snapshot}); err != nil {
		t.Fatalf("clean shop: %v %s", err, out.String())
	}
	dir := t.TempDir()
	for _, name := range []string{managedapp.FileName, "content.lock"} {
		data, err := os.ReadFile(filepath.Join(shop, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "content.lock" {
			data = []byte(strings.Replace(string(data), "contact-form-7 6.1.3 a5feaa9988f31cea6b052865d1c3ee080daa04ec23ffab7266a63f448e9c90d1",
				"contact-form-7 5.3.1 0e038161b0b42ed20aa60d0d4a6867c89f3d5c28ec056095c40bf2d3f0f6f9a9", 1))
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	err := runAppAdvisories(context.Background(), env, []string{"--spec", filepath.Join(dir, managedapp.FileName), "--snapshot", snapshot})
	if !errors.As(err, new(advisoriesFound)) || !strings.Contains(out.String(), "CVE-2020-35489") {
		t.Fatalf("vulnerable pin: %v %s", err, out.String())
	}
}
