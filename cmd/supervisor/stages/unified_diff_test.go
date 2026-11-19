package stages

import (
	"fmt"
	"strings"
	"testing"
)

func TestAUnifiedDiffIsAppliedToTheFilesItNames(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/README.md b/README.md\nindex 1..2 100644\n" +
		"--- a/README.md\n+++ b/README.md\n@@ -1,3 +1,3 @@\n # app\n-old line\n+new line\n \n" +
		"--- /dev/null\n+++ b/docs/new.md\n@@ -0,0 +1,2 @@\n+first\n+second\n"
	files, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(files) != 2 || files[0].newPath != "README.md" || files[1].oldPath != "" || files[1].newPath != "docs/new.md" {
		t.Fatalf("files = %+v", files)
	}
	got, err := files[0].apply("# app\nold line\n\nkept\n")
	if err != nil || got != "# app\nnew line\n\nkept\n" {
		t.Fatalf("apply = %q, %v", got, err)
	}
	got, err = files[1].apply("")
	if err != nil || got != "first\nsecond\n" {
		t.Fatalf("new file = %q, %v", got, err)
	}
}

func TestAPatchThatDoesNotMatchOrLeavesTheRepositoryIsRefused(t *testing.T) {
	t.Parallel()
	files, err := parseUnifiedDiff("--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files[0].apply("not a\n"); err == nil {
		t.Fatal("a hunk whose removed line differs from the file was applied")
	}
	for name, diff := range map[string]string{
		"climbs out":     "--- a/../etc/passwd\n+++ b/../etc/passwd\n@@ -1 +1 @@\n-a\n+b\n",
		"absolute":       "--- /etc/passwd\n+++ /etc/passwd\n@@ -1 +1 @@\n-a\n+b\n",
		"short hunk":     "--- a/x\n+++ b/x\n@@ -1,3 +1,3 @@\n a\n",
		"no file":        "@@ -1 +1 @@\n-a\n+b\n",
		"nothing at all": "just text\n",
	} {
		if _, err := parseUnifiedDiff(diff); err == nil {
			t.Errorf("%s: the patch was accepted", name)
		}
	}
}

// The refusal of a patch says why, so the Owner can tell an attempt that
// changed nothing from one that swept in too many or binary files.
func TestARefusedPatchSaysWhyItWasRefused(t *testing.T) {
	t.Parallel()
	var many strings.Builder
	for i := range maxPatchFiles + 1 {
		fmt.Fprintf(&many, "--- /dev/null\n+++ b/gen/f%d.txt\n@@ -0,0 +1 @@\n+x\n", i)
	}
	for name, c := range map[string]struct{ diff, want string }{
		"empty":          {"", "the agent made no change"},
		"many":           {many.String(), "changes 65 files, more than 64 (first: gen/f0.txt, gen/f1.txt"},
		"binary":         {"diff --git a/img/logo.png b/img/logo.png\nindex 1..2 100644\nGIT binary patch\nliteral 3\n", "binary file img/logo.png"},
		"binary summary": {"diff --git a/a.bin b/a.bin\nBinary files a/a.bin and b/a.bin differ\n", "binary file a.bin"},
	} {
		_, err := parseUnifiedDiff(c.diff)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.want)
		}
	}
}
