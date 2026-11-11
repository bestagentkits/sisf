package docs_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const (
	briefEN = "../../docs/self-improvement-software-factory-decision-brief.en.md"
	briefVI = "../../docs/self-improvement-software-factory-decision-brief.md"
	adrGlob = "../../docs/adr/*.md"
)

var (
	headingRe    = regexp.MustCompile(`^(#{2,4}) (\d+(?:\.\d+)*)?`)
	sisfIDRe     = regexp.MustCompile(`SISF-\d+`)
	orderedItem  = regexp.MustCompile(`^(\d+)\. `)
	firstCellIDs = regexp.MustCompile("^(SISF-\\d+|`[^`]+`)$")
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(string(data), "\n")
}

// outline lists every heading as "<level>:<number>" (number empty for unnumbered
// headings) and, under each heading, the numbers of ordered list items. Wording
// is ignored so the two languages can differ in text but not in structure.
func outline(lines []string) []string {
	var out []string
	inFence := false
	for _, l := range lines {
		if strings.HasPrefix(l, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := headingRe.FindStringSubmatch(l); m != nil {
			out = append(out, fmt.Sprintf("h%d:%s", len(m[1]), m[2]))
			continue
		}
		if m := orderedItem.FindStringSubmatch(l); m != nil {
			out = append(out, "item:"+m[1])
		}
	}
	return out
}

// tableRowIDs returns the first cell of each table row when that cell is an
// identifier (SISF-nn or a backticked name) rather than translatable prose.
func tableRowIDs(lines []string) []string {
	var out []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "|") {
			continue
		}
		cells := strings.Split(l, "|")
		if len(cells) < 3 {
			continue
		}
		first := strings.TrimSpace(cells[1])
		if firstCellIDs.MatchString(first) {
			out = append(out, first)
		}
	}
	return out
}

func sisfIDs(lines []string) []string {
	return sisfIDRe.FindAllString(strings.Join(lines, "\n"), -1)
}

func compare(t *testing.T, what string, en, vi []string) {
	t.Helper()
	if len(en) == 0 {
		t.Errorf("%s: no entries found in the English brief", what)
	}
	if !reflect.DeepEqual(en, vi) {
		t.Errorf("%s differ between EN and VI briefs\nEN: %v\nVI: %v", what, en, vi)
	}
}

func TestBriefsShareSectionNumbers(t *testing.T) {
	compare(t, "heading and list numbering", outline(readLines(t, briefEN)), outline(readLines(t, briefVI)))
}

func TestBriefsShareTableRowIDs(t *testing.T) {
	compare(t, "table row IDs", tableRowIDs(readLines(t, briefEN)), tableRowIDs(readLines(t, briefVI)))
}

func TestBriefsShareAcceptanceIDs(t *testing.T) {
	en, vi := sisfIDs(readLines(t, briefEN)), sisfIDs(readLines(t, briefVI))
	compare(t, "SISF IDs", en, vi)
	seen := map[string]bool{}
	for _, id := range en {
		seen[id] = true
	}
	for i := 1; i <= 20; i++ {
		if id := fmt.Sprintf("SISF-%02d", i); !seen[id] {
			t.Errorf("%s missing from the briefs", id)
		}
	}
}

func TestADRSetIsComplete(t *testing.T) {
	files, err := filepath.Glob(adrGlob)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 21 {
		t.Fatalf("want 21 ADR files, got %d: %v", len(files), files)
	}
	// laterProposed are the ADRs after the open points that await the
	// Owner's acceptance.
	laterProposed := map[int]bool{21: true}
	nameRe := regexp.MustCompile(`^(\d{4})-[a-z0-9]+(-[a-z0-9]+)*\.md$`)
	required := []string{"## Status", "## Context", "## Decision", "## Consequences", "## Acceptance test"}
	for i, f := range files {
		base := filepath.Base(f)
		m := nameRe.FindStringSubmatch(base)
		if m == nil {
			t.Errorf("%s: name must be NNNN-kebab-title.md", base)
			continue
		}
		if want := fmt.Sprintf("%04d", i+1); m[1] != want {
			t.Errorf("%s: want number %s (numbers must be contiguous from 0001)", base, want)
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, h := range required {
			if !strings.Contains(text, h) {
				t.Errorf("%s: missing section %q", base, h)
			}
		}
		proposed := strings.Contains(text, "Status: proposed")
		const lastOpenPointADR = 18
		if i+1 >= 10 && i+1 <= lastOpenPointADR && !proposed {
			t.Errorf("%s: open-point ADR must contain \"Status: proposed\"", base)
		}
		if i+1 > lastOpenPointADR && !laterProposed[i+1] && !strings.Contains(text, "Status: accepted") {
			t.Errorf("%s: ADR after the open points must contain \"Status: accepted\"", base)
		}
		if (i+1 < 10 || i+1 > lastOpenPointADR) && !laterProposed[i+1] && proposed {
			t.Errorf("%s: invariant ADR must not be proposed", base)
		}
	}
}
