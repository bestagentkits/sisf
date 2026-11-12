package api_test

import (
	"net/http"
	"os"
	"slices"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/internal/actions/apispec"
	"github.com/bestagentkits/sisf/pkg/core"
)

// TestEveryActionIsReachableAtItsOpenAPIPath checks the generated OpenAPI
// file against the registry and then calls each declared path.
func TestEveryActionIsReachableAtItsOpenAPIPath(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Action string `yaml:"x-sisf-action"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var registry *actions.Registry
	e := newEnv(t, func(*recorder) (*actions.Registry, error) {
		var rerr error
		registry, rerr = apispec.BuiltinRegistry()
		return registry, rerr
	})
	specs := registry.Specs()
	if len(specs) == 0 {
		t.Fatal("the registry holds no action")
	}
	var scopes []string
	for _, spec := range specs {
		scopes = append(scopes, spec.Scopes...)
	}
	sort.Strings(scopes)
	token := e.token(t, slices.Compact(scopes)...)
	b := e.browser(t)
	declared := map[string]bool{}
	for path, ops := range doc.Paths {
		declared[path] = true
		if _, ok := ops["post"]; !ok || len(ops) != 1 {
			t.Errorf("%s must declare exactly a POST operation", path)
		}
	}
	for _, spec := range specs {
		path := "/api/v1/actions/" + spec.Name
		if !declared[path] || doc.Paths[apispec.PathPrefix+spec.Name]["post"].Action != spec.Name {
			t.Errorf("%s is not declared in api/openapi.yaml at %s", spec.Name, path)
			continue
		}
		delete(declared, path)
		body := ""
		if spec.Mutates() {
			body = `{"dry_run":true}`
		}
		r := b.do(t, http.MethodPost, path, body, jsonHeader(bearer(token)))
		// Describe-only handlers refuse; the point is that the route ran the
		// action service and did not answer "no such action".
		if code := r.errorCode(t); code == core.CodeNotFound || code == core.CodeUnauthenticated {
			t.Errorf("%s answered %d %s", path, r.status, r.body)
		}
	}
	for path := range declared {
		t.Errorf("api/openapi.yaml declares %s, which the registry does not hold", path)
	}
}
