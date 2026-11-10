package install

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The images of Docker mode are built from files in build/. These checks read
// those files; no image is built.

var (
	digestPinnedFrom = regexp.MustCompile(`^FROM\s+\S+@sha256:[0-9a-f]{64}(\s+AS\s+\S+)?$`)
	numericUser      = regexp.MustCompile(`^USER\s+([1-9][0-9]*)(:[1-9][0-9]*)?$`)
	secretLooking    = regexp.MustCompile(`(?i)(password|secret|token|private[_-]?key|\.pem)`)
)

// dockerfileProblems lists what is wrong with a Dockerfile under the build
// rules: base images pinned by digest, static binaries, an unprivileged
// final user and nothing secret baked in.
func dockerfileProblems(text string) []string {
	var problems []string
	var froms int
	lastUser := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "FROM "):
			froms++
			if !digestPinnedFrom.MatchString(line) {
				problems = append(problems, "base image not pinned by digest: "+line)
			}
		case strings.HasPrefix(line, "USER "):
			lastUser = line
		case strings.HasPrefix(line, "ADD "):
			problems = append(problems, "ADD can fetch from the network; use COPY: "+line)
		case strings.HasPrefix(line, "ENV ") || strings.HasPrefix(line, "ARG "):
			if secretLooking.MatchString(line) {
				problems = append(problems, "a secret-looking value in the image: "+line)
			}
		case strings.HasPrefix(line, "COPY ") && !strings.Contains(line, "--from=") && secretLooking.MatchString(line):
			problems = append(problems, "a secret-looking file is copied in: "+line)
		}
		if strings.Contains(line, "go build") && !strings.Contains(line, "CGO_ENABLED=0") {
			problems = append(problems, "go build without CGO_ENABLED=0: "+line)
		}
	}
	if froms == 0 {
		problems = append(problems, "no FROM")
	}
	if !numericUser.MatchString(lastUser) {
		problems = append(problems, "the final user is not an unprivileged numeric user: "+lastUser)
	}
	if !strings.Contains(text, "CGO_ENABLED=0") {
		problems = append(problems, "the binary is not built with CGO_ENABLED=0")
	}
	return problems
}

func TestEveryImageOfDockerModeHasADockerfileThatKeepsTheBuildRules(t *testing.T) {
	t.Parallel()
	for _, img := range dockerImages {
		path := filepath.Join("..", "..", "build", img.dockerfile)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", img.name, err)
			continue
		}
		for _, p := range dockerfileProblems(string(data)) {
			t.Errorf("%s: %s", img.dockerfile, p)
		}
	}
}

func TestOnlyTheImagesOfDockerModeHaveDockerfiles(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("..", "..", "build", "*"))
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, img := range dockerImages {
		known[img.dockerfile] = true
	}
	for _, f := range files {
		if base := filepath.Base(f); !known[base] {
			t.Errorf("build/%s belongs to no image of Docker mode", base)
		}
	}
}

func TestTheBuildRulesRejectUnsafeDockerfiles(t *testing.T) {
	t.Parallel()
	const good = "FROM golang:1.25-alpine@sha256:" + "1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59" + " AS build\n" +
		"RUN CGO_ENABLED=0 go build -o /out/x ./cmd/x\nFROM alpine:3.22@sha256:" + "49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f" + "\nUSER 10001:10001\n"
	if p := dockerfileProblems(good); len(p) != 0 {
		t.Fatalf("a sound Dockerfile was rejected: %v", p)
	}
	for name, bad := range map[string]string{
		"floating tag":      strings.Replace(good, "@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f", "", 1),
		"short digest":      strings.Replace(good, "49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f", "49340b34", 1),
		"cgo on":            strings.ReplaceAll(good, "CGO_ENABLED=0", "CGO_ENABLED=1"),
		"build without cgo": strings.ReplaceAll(good, "CGO_ENABLED=0 go build", "go build"),
		"root user":         strings.Replace(good, "USER 10001:10001", "USER root", 1),
		"user zero":         strings.Replace(good, "USER 10001:10001", "USER 0", 1),
		"no user":           strings.Replace(good, "USER 10001:10001\n", "", 1),
		"remote ADD":        good + "ADD https://example.test/x /x\n",
		"secret env":        good + "ENV GITHUB_TOKEN=abc\n",
		"secret file":       good + "COPY secrets/root-key.pem /k\n",
		"no FROM":           "USER 10001\n",
	} {
		if len(dockerfileProblems(bad)) == 0 {
			t.Errorf("%s: an unsafe Dockerfile passed", name)
		}
	}
}
