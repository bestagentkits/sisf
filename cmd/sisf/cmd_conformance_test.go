package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	repoRoot = "../.."
	testdata = repoRoot + "/internal/conformance/testdata"
)

// sisf runs the real command line, as registered, with the given arguments.
func sisf(t *testing.T, args ...string) (status int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	status = commands.run(context.Background(), &Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut}, args)
	return status, out.String(), errOut.String()
}

func TestConformanceCheckAcceptsTheValidExamples(t *testing.T) {
	for _, path := range []string{repoRoot + "/.sisf/factory.yaml", testdata + "/valid-docker.yaml", testdata + "/valid-standalone.yaml"} {
		status, stdout, stderr := sisf(t, "conformance", "check", path)
		if status != exitOK || stderr != "" || !strings.Contains(stdout, path+": conformant") || !strings.Contains(stdout, "self-update: enabled") {
			t.Errorf("%s: status %d, stdout %q, stderr %q", path, status, stdout, stderr)
		}
	}
}

func TestConformanceCheckRejectsEveryRejectFixtureWithANonZeroStatus(t *testing.T) {
	fixtures, err := filepath.Glob(testdata + "/reject-*.yaml")
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no reject fixtures found: %v", err)
	}
	for _, path := range fixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			status, stdout, stderr := sisf(t, "conformance", "check", path)
			if status != exitFailure || stdout != "" {
				t.Fatalf("status %d, stdout %q; want status %d and nothing on stdout", status, stdout, exitFailure)
			}
			if !strings.Contains(stderr, "sisf: INVALID_ARGUMENT: ") {
				t.Errorf("the failure must be typed, got %q", stderr)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, rest, _ := strings.Cut(string(data), "# expect: ")
			rule, _, _ := strings.Cut(rest, "\n")
			if rule == "unreadable" {
				return
			}
			// The rule, the place and the fix make the error actionable.
			if !strings.Contains(stderr, "["+rule+"] ") || !strings.Contains(stderr, "fix: ") || !strings.Contains(stderr, "next: conformance.check") {
				t.Errorf("want rule %s with a fix and a next step, got %q", rule, stderr)
			}
		})
	}
}

func TestConformanceCheckNamesTheMissingZone(t *testing.T) {
	status, _, stderr := sisf(t, "conformance", "check", testdata+"/reject-missing-zone-agent.yaml")
	for _, want := range []string{"not conformant", "[zone.missing] components.agent", "Zone 2 (agent) has no component", "fix: declare components.agent", "1 finding"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if status != exitFailure {
		t.Errorf("status %d, want %d", status, exitFailure)
	}
}

func TestConformanceCheckSaysWhenSelfUpdateIsDisabled(t *testing.T) {
	valid, err := os.ReadFile(testdata + "/valid-docker.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// No supervisor block, and nothing the factory publishes by itself.
	head, tail, found := strings.Cut(string(valid), "supervisor:\n  image:")
	_, tail, _ = strings.Cut(tail, "networks:\n")
	text := strings.Replace(head+"networks:\n"+tail, "units: [app]", "units: []", 1)
	if !found || strings.Contains(text, "trust_volume") {
		t.Fatal("the supervisor block was not removed")
	}
	path := filepath.Join(t.TempDir(), "factory.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := sisf(t, "conformance", "check", path)
	if status != exitOK || !strings.Contains(stdout, "self-update: disabled") {
		t.Fatalf("status %d, stdout %q, stderr %q", status, stdout, stderr)
	}
}

func TestConformanceCheckDefaultsToTheManifestOfTheWorkingDirectory(t *testing.T) {
	t.Chdir(repoRoot)
	if status, stdout, stderr := sisf(t, "conformance", "check"); status != exitOK || !strings.Contains(stdout, ".sisf/factory.yaml: conformant") {
		t.Fatalf("in the repository: status %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	t.Chdir(t.TempDir())
	if status, _, stderr := sisf(t, "conformance", "check"); status != exitFailure || !strings.Contains(stderr, "sisf: NOT_FOUND: manifest .sisf/factory.yaml does not exist") {
		t.Fatalf("without a manifest: status %d, stderr %q", status, stderr)
	}
}

func TestConformanceCheckRefusesAWrongInvocation(t *testing.T) {
	for _, args := range [][]string{{"a.yaml", "b.yaml"}, {"--strict"}} {
		status, stdout, stderr := sisf(t, append([]string{"conformance", "check"}, args...)...)
		if status != exitUsage || stdout != "" || !strings.Contains(stderr, "Usage: sisf conformance check [file]") {
			t.Errorf("%v: status %d, stderr %q", args, status, stderr)
		}
	}
	if status, _, stderr := sisf(t, "conformance"); status != exitUsage || !strings.Contains(stderr, "conformance check") {
		t.Errorf("the group alone: status %d, stderr %q", status, stderr)
	}
	if status, stdout, _ := sisf(t, "help", "conformance", "check"); status != exitOK || !strings.Contains(stdout, "all three zones") {
		t.Errorf("help: status %d, stdout %q", status, stdout)
	}
}
