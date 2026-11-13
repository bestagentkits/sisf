package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

func runAdoptForTest(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runAdopt(context.Background(), &Env{Stdout: &out, Stderr: &out}, args)
	return out.String(), err
}

func TestAdoptWritesAValidSpecForTheShop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, managedapp.FileName)
	msg, err := runAdoptForTest(t, "../../examples/woocommerce-app", "--name", "shop", "--out", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "as a wordpress application") || !strings.Contains(msg, "review:") {
		t.Fatalf("output: %s", msg)
	}
	if _, err := managedapp.Load(out); err != nil {
		t.Fatalf("the written Spec does not load: %v", err)
	}
	if _, err := runAdoptForTest(t, "../../examples/woocommerce-app", "--out", out); err == nil {
		t.Fatal("an existing Spec was replaced without --force")
	}
}

// A secret given on the command line is refused before anything is written.
func TestAdoptRefusesASecretOnTheCommandLine(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), managedapp.FileName)
	_, err := runAdoptForTest(t, "--env", "API_TOKEN=abc", "--out", out, "--root", "../..", "../../examples/hello-app")
	if err == nil || !strings.Contains(err.Error(), "looks like a secret") || strings.Contains(err.Error(), "abc") {
		t.Fatalf("want a refusal that does not echo the value, got %v", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("a Spec was written")
	}
}
