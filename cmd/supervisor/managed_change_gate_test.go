package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// commitFiles is the repository at two commits: path -> content per commit.
type commitFiles struct {
	files map[core.CommitSHA]map[string]string
	err   error
}

func (c commitFiles) ReadFile(_ context.Context, commit core.CommitSHA, path string) ([]byte, error) {
	if c.err != nil {
		return nil, c.err
	}
	data, ok := c.files[commit][path]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "no such file")
	}
	return []byte(data), nil
}

const (
	baseCommit      core.CommitSHA = "1111111111111111111111111111111111111111"
	candidateCommit core.CommitSHA = "2222222222222222222222222222222222222222"
)

func shopGate(t *testing.T, candidateLock string, readErr error) managedChangeGate {
	t.Helper()
	spec, err := managedapp.Load(shopSpecPath(t))
	if err != nil {
		t.Fatal(err)
	}
	files := map[core.CommitSHA]map[string]string{baseCommit: {spec.Bundle.Lock: baseLock(t)}, candidateCommit: {}}
	if candidateLock != "" {
		files[candidateCommit][spec.Bundle.Lock] = candidateLock
	}
	return managedChangeGate{spec: spec, tree: commitFiles{files: files, err: readErr}}
}

func baseLock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../examples/woocommerce-app/content.lock")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A Core upgrade, a protected file and a lockfile the candidate removed
// are the Owner's; a plugin's patch update the factory releases alone.
func TestTheShopsChangeGateJudgesFromTheRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lock := baseLock(t)
	cases := map[string]struct {
		candidateLock string
		changed       []string
		owner         bool
		says          string
	}{
		"a Core upgrade":         {strings.Replace(lock, "core wordpress 7.0.4", "core wordpress 7.0.8", 1), []string{"content.lock"}, true, "7.0.8"},
		"a protected file":       {lock, []string{"wp-config.php"}, true, "wp-config.php"},
		"a removed lockfile":     {"", []string{"content.lock"}, true, "cannot be read"},
		"an unreadable lockfile": {"not a lock line\n", []string{"content.lock"}, true, "cannot be read"},
		"a plugin patch":         {strings.Replace(lock, "contact-form-7 6.1.3", "contact-form-7 6.1.4", 1), []string{"content.lock"}, false, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			v, err := shopGate(t, c.candidateLock, nil).Judge(ctx, baseCommit, candidateCommit, c.changed)
			if err != nil || v.Owner != c.owner || !strings.Contains(strings.Join(v.Reasons, "; "), c.says) {
				t.Fatalf("verdict %+v, %v; want owner %v saying %q", v, err, c.owner, c.says)
			}
		})
	}
	if _, err := shopGate(t, lock, core.NewError(core.CodeUnavailable, "GitHub is down")).Judge(ctx, baseCommit, candidateCommit, []string{"content.lock"}); err == nil {
		t.Fatal("an outage of the repository was judged")
	}
}
