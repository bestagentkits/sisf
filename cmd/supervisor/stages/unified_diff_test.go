package stages

import "testing"

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
