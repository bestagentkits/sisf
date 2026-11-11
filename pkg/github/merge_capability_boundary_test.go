package github_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/github"
)

// Only the supervisor can merge into a default branch. Neither the client
// the agent and control zones are handed nor the REST client itself has a
// merge method, and the one type that merges is built and named nowhere in
// the product but in the supervisor's command (and the test kit, which
// wires a whole factory the way the supervisor's command does).
func TestOnlyTheSupervisorHasAWayToMerge(t *testing.T) {
	t.Parallel()
	for _, typ := range []reflect.Type{reflect.TypeFor[github.Client](), reflect.TypeFor[*github.RESTClient]()} {
		for i := range typ.NumMethod() {
			if name := typ.Method(i).Name; strings.Contains(strings.ToLower(name), "merge") {
				t.Errorf("%s has the method %s", typ, name)
			}
		}
	}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "node_modules" || rel == "ui/node_modules" || strings.HasPrefix(rel, ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "pkg/github/") ||
			strings.HasPrefix(rel, "cmd/supervisor/") || strings.HasPrefix(rel, "internal/testkit/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if text := string(src); strings.Contains(text, "github.NewMerger") || strings.Contains(text, "github.Merger") ||
			strings.Contains(text, "github.MergeRequest") {
			t.Errorf("%s reaches the merger; only cmd/supervisor may", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
