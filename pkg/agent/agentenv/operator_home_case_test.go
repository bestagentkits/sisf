package agentenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPathsDifferingOnlyInLetterCaseOverlapWhereTheFileSystemIgnoresCase(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		a, b       string
		fold, want bool
	}{
		"same case":                {"/srv/sisf/trust", "/srv/sisf/trust", false, true},
		"another case, folded":     {"/srv/SISF/Trust", "/srv/sisf/trust", true, true},
		"inside, another case":     {"/srv/SISF/Trust/home", "/srv/sisf/trust", true, true},
		"holding, another case":    {"/SRV", "/srv/sisf/trust", true, true},
		"another case, not folded": {"/srv/SISF/Trust", "/srv/sisf/trust", false, false},
		"apart, folded":            {"/srv/sisf/home", "/srv/sisf/trust", true, false},
		"a sibling prefix, folded": {"/srv/sisf/TRUSTED", "/srv/sisf/trust", true, false},
	} {
		if got := overlaps(c.a, c.b, c.fold); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	if want := runtime.GOOS == "darwin" || runtime.GOOS == "windows"; caseInsensitivePaths != want {
		t.Errorf("letter case is ignored: %v on %s", caseInsensitivePaths, runtime.GOOS)
	}
}

// On macOS a home named in another letter case than the trust directory
// is that directory, and is refused.
func TestOnDarwinAHomeInAnotherCaseOfTheTrustDirectoryIsRefused(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("letter case is only ignored on macOS and Windows file systems")
	}
	trust := filepath.Join(t.TempDir(), "Trust")
	if err := os.Mkdir(trust, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(trust), strings.ToUpper(filepath.Base(trust)))
	if !Overlaps(other, trust) {
		t.Fatalf("%s and %s do not overlap", other, trust)
	}
	if err := CheckOperatorHome(other, trust); err == nil {
		t.Fatalf("the home %s was accepted beside the trust directory %s", other, trust)
	}
}
