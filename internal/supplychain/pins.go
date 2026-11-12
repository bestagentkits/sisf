package supplychain

import (
	"fmt"
	"regexp"
	"strings"
)

// Module is one Go module at one version.
type Module struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// String renders "path@version".
func (m Module) String() string { return m.Path + "@" + m.Version }

// goMod is what the gate reads from a go.mod file.
type goMod struct {
	// requires maps each required module path to its version.
	requires map[string]string
	// problems are pins the file breaks.
	problems []string
}

var (
	// moduleVersionPattern is a semantic version, a pseudo-version included,
	// as go.mod writes it. Branch names and "latest" never match.
	moduleVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+incompatible)?$`)
	digestPinPattern     = regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)
	actionPinPattern     = regexp.MustCompile(`^[^@\s]+@[a-f0-9]{40}$`)
	usesPattern          = regexp.MustCompile(`(?m)^\s*(?:-\s*)?uses:\s*["']?([^"'\s#]+)`)
	imagePattern         = regexp.MustCompile(`(?m)^\s*(?:-\s*)?image:\s*["']?([^"'\s#]+)`)
)

// parseGoMod reads the require and replace directives of a go.mod file. A
// requirement without an exact version, or a replacement by a local
// directory, which no hash pins, is a problem.
func parseGoMod(file string, data []byte) goMod {
	mod := goMod{requires: map[string]string{}}
	block := ""
	for n, raw := range strings.Split(string(data), "\n") {
		line, _, _ := strings.Cut(raw, "//")
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case block != "" && line == ")":
			block = ""
			continue
		case block == "" && strings.HasSuffix(line, "("):
			block = strings.TrimSpace(strings.TrimSuffix(line, "("))
			continue
		}
		directive, rest := block, line
		if block == "" {
			directive, rest, _ = strings.Cut(line, " ")
		}
		at := fmt.Sprintf("%s:%d", file, n+1)
		switch directive {
		case "require":
			fields := strings.Fields(rest)
			if len(fields) != 2 || !moduleVersionPattern.MatchString(fields[1]) {
				mod.problems = append(mod.problems, at+": requirement is not pinned to an exact version")
				continue
			}
			mod.requires[fields[0]] = fields[1]
		case "replace":
			_, target, ok := strings.Cut(rest, "=>")
			fields := strings.Fields(target)
			if !ok || len(fields) != 2 || !moduleVersionPattern.MatchString(fields[1]) {
				mod.problems = append(mod.problems, at+": replacement is not pinned to a module version")
			}
		}
	}
	return mod
}

// parseGoSum returns the "path@version" pairs a go.sum file pins, and the
// lines that are not well-formed hashes.
func parseGoSum(file string, data []byte) (map[string]bool, []string) {
	pinned := map[string]bool{}
	var problems []string
	for n, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 || !strings.HasPrefix(fields[2], "h1:") {
			problems = append(problems, fmt.Sprintf("%s:%d: line is not a module hash", file, n+1))
			continue
		}
		if _, ok := moduleHash(fields[2]); !ok {
			problems = append(problems, fmt.Sprintf("%s:%d: module hash is malformed", file, n+1))
			continue
		}
		pinned[fields[0]+"@"+strings.TrimSuffix(fields[1], "/go.mod")] = true
	}
	return pinned, problems
}

// checkContainerfile requires every base image to be pinned by digest. A
// FROM of an earlier stage or of scratch has nothing to pin; a FROM built
// from a build argument cannot be verified and is refused.
func checkContainerfile(file string, data []byte) []string {
	var problems []string
	stages := map[string]bool{}
	joined := strings.ReplaceAll(string(data), "\\\n", " ")
	for n, line := range strings.Split(joined, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		args := fields[1:]
		for len(args) > 0 && strings.HasPrefix(args[0], "--") {
			args = args[1:]
		}
		if len(args) == 0 {
			problems = append(problems, fmt.Sprintf("%s:%d: FROM names no image", file, n+1))
			continue
		}
		image := args[0]
		switch {
		case strings.EqualFold(image, "scratch") || stages[strings.ToLower(image)]:
		case strings.Contains(image, "$"):
			problems = append(problems, fmt.Sprintf("%s:%d: base image comes from a variable and cannot be verified", file, n+1))
		case !digestPinPattern.MatchString(image):
			problems = append(problems, fmt.Sprintf("%s:%d: base image %s is not pinned by a sha256 digest", file, n+1, image))
		}
		// The stage name is usable from the next FROM on, not this one.
		if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
			stages[strings.ToLower(args[2])] = true
		}
	}
	return problems
}

// checkCIConfig requires every action a workflow uses to be pinned to a full
// commit, and every container image to a digest. Local actions need no pin.
func checkCIConfig(file string, data []byte) []string {
	if !strings.HasSuffix(file, ".yml") && !strings.HasSuffix(file, ".yaml") {
		return nil
	}
	var problems []string
	for _, m := range usesPattern.FindAllStringSubmatch(string(data), -1) {
		ref := m[1]
		switch {
		case strings.HasPrefix(ref, "./"):
		case strings.HasPrefix(ref, "docker://"):
			if !digestPinPattern.MatchString(ref) {
				problems = append(problems, fmt.Sprintf("%s: container action %s is not pinned by a sha256 digest", file, ref))
			}
		case !actionPinPattern.MatchString(ref):
			problems = append(problems, fmt.Sprintf("%s: action %s is not pinned to a full commit", file, ref))
		}
	}
	for _, m := range imagePattern.FindAllStringSubmatch(string(data), -1) {
		if !digestPinPattern.MatchString(m[1]) {
			problems = append(problems, fmt.Sprintf("%s: image %s is not pinned by a sha256 digest", file, m[1]))
		}
	}
	return problems
}
