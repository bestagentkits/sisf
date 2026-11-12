//go:build smoke

package smoke

import (
	"fmt"
	"testing"
)

// The real smoke run touches exactly one GitHub repository, named by the
// operator through repoEnv. Any other value, including a differently cased
// or decorated spelling of the sandbox, is refused before anything happens.
const (
	sandboxRepo = "bestagentkits/sisf-sandbox"
	repoEnv     = "SISF_SMOKE_REPO"
)

// checkSandboxRepo accepts only the sandbox repository, spelled exactly.
func checkSandboxRepo(value string) error {
	if value != sandboxRepo {
		return fmt.Errorf("%s=%q is refused: the real smoke run only ever touches %s", repoEnv, value, sandboxRepo)
	}
	return nil
}

func TestSandboxRepoGuardRefusesEveryOtherRepository(t *testing.T) {
	t.Parallel()
	refused := []string{
		"",
		" " + sandboxRepo,
		sandboxRepo + " ",
		sandboxRepo + "\n",
		"BestAgentKits/sisf-sandbox",
		"bestagentkits/SISF-SANDBOX",
		"bestagentkits/sisf",
		"bestagentkits/sisf-sandbox2",
		"other/sisf-sandbox",
		sandboxRepo + "/extra",
		"https://github.com/" + sandboxRepo,
		"github.com/" + sandboxRepo,
		sandboxRepo + "#1",
	}
	for _, value := range refused {
		if err := checkSandboxRepo(value); err == nil {
			t.Errorf("checkSandboxRepo(%q) accepted a repository other than the sandbox", value)
		}
	}
	if err := checkSandboxRepo(sandboxRepo); err != nil {
		t.Fatalf("checkSandboxRepo refused the sandbox itself: %v", err)
	}
}
