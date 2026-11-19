package stages

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// maxPatchFiles bounds the files one patch may touch.
const maxPatchFiles = 64

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// filePatch is the change of one file in a unified diff.
type filePatch struct {
	// oldPath and newPath are repository-relative; "" stands for /dev/null.
	oldPath, newPath string
	hunks            []hunk
}

type hunk struct {
	oldStart int
	lines    []string // each with its ' ', '-' or '+' prefix
}

// parseUnifiedDiff reads a unified diff as git and the agents write it. It
// is data from an agent: anything it cannot read exactly is refused.
func parseUnifiedDiff(diff string) ([]filePatch, error) {
	var files []filePatch
	block := "" // the path named by the current "diff --git" header
	lines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "diff --git "):
			block = gitHeaderPath(line)
		case strings.HasPrefix(line, "GIT binary patch") || strings.HasPrefix(line, "Binary files "):
			return nil, fmt.Errorf("line %d: the patch changes binary file %s, which cannot be applied", i+1, orUnnamed(block))
		case strings.HasPrefix(line, "--- "):
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
				return nil, fmt.Errorf("line %d: a --- header must be followed by +++", i+1)
			}
			oldPath, err := diffPath(line[4:])
			if err != nil {
				return nil, err
			}
			newPath, err := diffPath(lines[i+1][4:])
			if err != nil {
				return nil, err
			}
			if oldPath == "" && newPath == "" {
				return nil, fmt.Errorf("line %d: a file cannot be /dev/null on both sides", i+1)
			}
			files = append(files, filePatch{oldPath: oldPath, newPath: newPath})
			i++
		case strings.HasPrefix(line, "@@"):
			if len(files) == 0 {
				return nil, fmt.Errorf("line %d: a hunk outside any file", i+1)
			}
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("line %d: unreadable hunk header", i+1)
			}
			oldStart, _ := strconv.Atoi(m[1])
			oldCount, newCount := count(m[2]), count(m[4])
			h := hunk{oldStart: oldStart}
			for i+1 < len(lines) && (oldCount > 0 || newCount > 0) {
				i++
				body := lines[i]
				if body == "" {
					body = " " // an empty context line whose space was trimmed
				}
				switch body[0] {
				case ' ':
					oldCount--
					newCount--
				case '-':
					oldCount--
				case '+':
					newCount--
				case '\\':
					continue // "\ No newline at end of file"
				default:
					return nil, fmt.Errorf("line %d: unexpected line in a hunk", i+1)
				}
				h.lines = append(h.lines, body)
			}
			if oldCount != 0 || newCount != 0 {
				return nil, fmt.Errorf("line %d: the hunk is shorter than its header says", i+1)
			}
			files[len(files)-1].hunks = append(files[len(files)-1].hunks, h)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("the patch changes no file: the agent made no change (a patch must change between 1 and %d files)", maxPatchFiles)
	}
	if len(files) > maxPatchFiles {
		return nil, fmt.Errorf("the patch changes %d files, more than %d (first: %s)", len(files), maxPatchFiles, firstPaths(files, 5))
	}
	return files, nil
}

// gitHeaderPath is the new path of a "diff --git a/X b/Y" header, or "" when
// it cannot be read plainly.
func gitHeaderPath(line string) string {
	_, b, ok := strings.Cut(strings.TrimPrefix(line, "diff --git "), " b/")
	if !ok {
		return ""
	}
	return b
}

func orUnnamed(path string) string {
	if path == "" {
		return "(unnamed)"
	}
	return path
}

// firstPaths names the first n files of a patch, for a refusal message.
func firstPaths(files []filePatch, n int) string {
	var names []string
	for _, f := range files[:min(n, len(files))] {
		names = append(names, orUnnamed(cmp.Or(f.newPath, f.oldPath)))
	}
	return strings.Join(names, ", ")
}

func count(raw string) int {
	if raw == "" {
		return 1
	}
	n, _ := strconv.Atoi(raw)
	return n
}

// diffPath reads the path of a ---/+++ header without its a/ or b/ prefix.
func diffPath(raw string) (string, error) {
	raw, _, _ = strings.Cut(raw, "\t")
	if raw == "/dev/null" {
		return "", nil
	}
	if p, ok := strings.CutPrefix(raw, "a/"); ok {
		raw = p
	} else if p, ok := strings.CutPrefix(raw, "b/"); ok {
		raw = p
	}
	if !cleanRepoPath(raw) {
		return "", fmt.Errorf("path %q is not a plain path inside the repository", raw)
	}
	return raw, nil
}

// cleanRepoPath accepts a relative path with no empty, "." or ".." segment.
func cleanRepoPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	return !slices.ContainsFunc(strings.Split(p, "/"), func(s string) bool { return s == "" || s == "." || s == ".." })
}

// apply applies the hunks of fp to old, the file's content before the
// change ("" for a new file). Every context and removed line must match.
func (fp filePatch) apply(old string) (string, error) {
	src := splitLines(old)
	var out []string
	next := 0 // index in src of the next line not yet copied
	for _, h := range fp.hunks {
		start := max(h.oldStart-1, 0)
		if start < next || start > len(src) {
			return "", fmt.Errorf("%s: a hunk starts outside the file", fp.newPath)
		}
		out = append(out, src[next:start]...)
		next = start
		for _, l := range h.lines {
			switch l[0] {
			case ' ', '-':
				if next >= len(src) || src[next] != l[1:] {
					return "", fmt.Errorf("%s: the patch does not match the file at line %d", fp.newPath, next+1)
				}
				if l[0] == ' ' {
					out = append(out, src[next])
				}
				next++
			case '+':
				out = append(out, l[1:])
			}
		}
	}
	out = append(out, src[next:]...)
	if len(out) == 0 {
		return "", nil
	}
	return strings.Join(out, "\n") + "\n", nil
}

// splitLines splits content into lines without their line ends.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}
