package supervisor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// A directory of records on the trust volume is found again once it exists:
// a supervisor that created it must be able to start a second time.
func TestATrustDirectoryIsAcceptedBeforeAndAfterItExists(t *testing.T) {
	volume := t.TempDir()
	if err := os.Chmod(volume, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := TrustDirectory(volume, "local-slots")
	if err != nil {
		t.Fatalf("before the directory exists: %v", err)
	}
	if err := os.MkdirAll(first, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "blue.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := TrustDirectory(volume, "local-slots")
	if err != nil {
		t.Fatalf("once the directory exists: %v", err)
	}
	if second != first {
		t.Fatalf("the directory moved from %s to %s", first, second)
	}
}

func TestATrustDirectoryThatIsNotADirectoryOfItsOwnIsRefused(t *testing.T) {
	volume := t.TempDir()
	if err := os.Chmod(volume, 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(volume, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(volume, "link")); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(volume, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	names := []string{"file", "link", "..", ".", filepath.Join("a", "b"), filepath.Join("..", "out")}
	if runtime.GOOS != "windows" {
		names = append(names, "open")
	}
	for _, name := range names {
		if got, err := TrustDirectory(volume, name); core.CodeOf(err) != core.CodePermissionDenied {
			t.Errorf("TrustDirectory(%q) = %q, %v; want PERMISSION_DENIED", name, got, err)
		}
	}
}
