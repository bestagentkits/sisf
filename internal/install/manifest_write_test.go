package install

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A manifest is written whole beside the old one and renamed over it, so
// a crash or a full disk never leaves a cut manifest: the old file, still
// reached by a hard link, keeps its content, the mode an existing file
// had is kept, a new file is readable by all, and no temporary file is
// left beside it, also when the write fails.
func TestTheManifestIsReplacedWholeNeverRewrittenInPlace(t *testing.T) {
	t.Parallel()
	o, err := testOptions(manifest.ModeStandalone).normalize()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.yaml")
	if err := writeManifest(path, buildManifest(o)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("a new manifest: %v, %v; want mode 0644", info, err)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "held")
	if err := os.Link(path, link); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	changed := buildManifest(o)
	changed.FactoryID = "changed"
	if err := writeManifest(path, changed); err != nil {
		t.Fatal(err)
	}
	if held, err := os.ReadFile(link); err != nil || !bytes.Equal(held, old) {
		t.Fatal("the old manifest was rewritten in place, not replaced")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("the rewritten manifest: %v, %v; want the mode 0640 the file had", info, err)
	}
	if back, err := manifest.ParseFile(path); err != nil || back.FactoryID != "changed" {
		t.Fatalf("the rewritten manifest reads back as %v, %v", back, err)
	}
	blocked := filepath.Join(dir, "blocked.yaml")
	if err := os.MkdirAll(filepath.Join(blocked, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(blocked, changed); err == nil {
		t.Fatal("a manifest was written over a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "factory.yaml" && e.Name() != "blocked.yaml" {
			t.Errorf("%s was left beside the manifest", e.Name())
		}
	}
}

// The containers of a Docker install mount the manifest as one file, and a
// mount follows the file, not its name: a changed manifest must reach the
// file a running container already holds, and an unchanged one must leave
// that file alone.
func TestADockerManifestIsWrittenIntoTheFileItsContainersMount(t *testing.T) {
	t.Parallel()
	o, err := testOptions(manifest.ModeDocker).normalize()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "factory.yaml")
	if err := writeManifest(path, buildManifest(o)); err != nil {
		t.Fatal(err)
	}
	mounted := filepath.Join(t.TempDir(), "mounted")
	if err := os.Link(path, mounted); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(path, buildManifest(o)); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(path); err != nil || !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("an unchanged manifest was written again: %v", err)
	}
	changed := buildManifest(o)
	changed.FactoryID = "changed"
	if err := writeManifest(path, changed); err != nil {
		t.Fatal(err)
	}
	if back, err := manifest.ParseFile(mounted); err != nil || back.FactoryID != "changed" {
		t.Fatalf("the mounted file reads back as %v, %v; want the changed manifest", back, err)
	}
}
