package manifest_test

import (
	"bytes"
	"flag"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// publishedSchema is the copy editors and other tools fetch.
const publishedSchema = "../../schemas/factory.schema.json"

var update = flag.Bool("update", false, "rewrite schemas/factory.schema.json from the manifest types")

// The published file is generated, never edited: after a change to the
// manifest types, run `go test ./pkg/manifest -run PublishedSchema -update`.
func TestPublishedSchemaIsTheOneGeneratedFromTheManifestTypes(t *testing.T) {
	want, err := manifest.SchemaJSON()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(publishedSchema, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(publishedSchema)
	if err != nil {
		t.Fatalf("the published schema must exist: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date; regenerate it with -update", publishedSchema)
	}
}

// dig follows keys into a schema document.
func dig(t *testing.T, doc map[string]any, keys ...string) any {
	t.Helper()
	var node any = doc
	for _, key := range keys {
		object, ok := node.(map[string]any)
		if !ok || object[key] == nil {
			t.Fatalf("the schema has nothing at %s", strings.Join(keys, "/"))
		}
		node = object[key]
	}
	return node
}

func TestSchemaListsExactlyTheKeysOfTheManifestTypes(t *testing.T) {
	schema := manifest.Schema()
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			walk(typ.Elem())
			return
		case reflect.Struct:
		default:
			return
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		var want []string
		for i := range typ.NumField() {
			key, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			want = append(want, key)
			walk(typ.Field(i).Type)
		}
		object := schema
		if typ != reflect.TypeOf(manifest.Manifest{}) {
			object = dig(t, schema, "$defs", typ.Name()).(map[string]any)
		}
		var got []string
		for key := range dig(t, object, "properties").(map[string]any) {
			got = append(got, key)
		}
		sort.Strings(want)
		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: schema keys %v, type keys %v", typ.Name(), got, want)
		}
		if object["additionalProperties"] != false {
			t.Errorf("%s must refuse keys it does not define", typ.Name())
		}
	}
	walk(reflect.TypeOf(manifest.Manifest{}))
	if len(seen) < 20 {
		t.Fatalf("only %d types were compared", len(seen))
	}
}

func TestSchemaClosedSetsHoldEveryValueTheTypesAccept(t *testing.T) {
	schema := manifest.Schema()
	cases := []struct {
		at    []string
		count int
		valid func(string) bool
	}{
		{[]string{"properties", "mode", "enum"}, 2, func(v string) bool { return manifest.Mode(v).Valid() }},
		{[]string{"$defs", "Unit", "properties", "lifecycle", "enum"}, 3, func(v string) bool { return manifest.Lifecycle(v).Valid() }},
		{[]string{"$defs", "Unit", "properties", "networks", "items", "enum"}, 4, func(v string) bool { return manifest.NetworkName(v).Valid() }},
		{[]string{"$defs", "Supervisor", "properties", "digest_change", "enum"}, 2, func(v string) bool { return manifest.DigestChange(v).Valid() }},
		{[]string{"$defs", "StateStore", "properties", "driver", "enum"}, 2, func(v string) bool { return manifest.StateDriver(v).Valid() }},
		{[]string{"$defs", "ArtifactStore", "properties", "kind", "enum"}, 2, func(v string) bool { return manifest.ArtifactsKind(v).Valid() }},
		{[]string{"$defs", "Release", "properties", "strategy", "enum"}, 2, func(v string) bool { return manifest.Strategy(v).Valid() }},
		{[]string{"$defs", "Release", "properties", "policy", "enum"}, 3, func(v string) bool { return core.DelegationMode(v).Valid() }},
		{[]string{"$defs", "Release", "properties", "units", "items", "enum"}, 9, func(v string) bool { return manifest.UnitName(v).Valid() }},
		{[]string{"$defs", "Budget", "properties", "mode", "enum"}, 2, func(v string) bool { return core.BudgetMode(v).Valid() }},
		{[]string{"$defs", "Budget", "properties", "period", "enum"}, 2, func(v string) bool { return manifest.BudgetPeriod(v).Valid() }},
		{[]string{"$defs", "EgressPolicy", "properties", "mode", "enum"}, 2, func(v string) bool { return core.EgressMode(v).Valid() }},
		{[]string{"$defs", "Volume", "properties", "zone", "enum"}, 4, func(v string) bool { return core.Zone(v).Valid() }},
	}
	for _, tc := range cases {
		values := dig(t, schema, tc.at...).([]string)
		if len(values) != tc.count {
			t.Errorf("%v lists %v, want %d values", tc.at, values, tc.count)
		}
		for _, v := range values {
			if !tc.valid(v) {
				t.Errorf("%v lists %q, which the type rejects", tc.at, v)
			}
		}
	}
	// Maps keyed by a closed set of names declare exactly those names.
	keyed := map[string]int{"units": 9, "networks": 4}
	for key, count := range keyed {
		if got := len(dig(t, schema, "properties", key, "properties").(map[string]any)); got != count {
			t.Errorf("%s declares %d names, want %d", key, got, count)
		}
		if got := len(dig(t, schema, "properties", key, "required").([]string)); got != count {
			t.Errorf("%s requires %d names, want all %d", key, got, count)
		}
	}
	if got := dig(t, schema, "$defs", "Components", "required").([]string); len(got) != 3 {
		t.Errorf("all three zones must be required, got %v", got)
	}
	for kind := range dig(t, schema, "$defs", "ModelRoutes", "properties", "routes", "properties").(map[string]any) {
		if !core.StepKind(kind).Valid() {
			t.Errorf("model routes accept unknown step kind %q", kind)
		}
	}
}

// The schema repeats, as text patterns, rules that validators in code also
// enforce. Both must give the same answer or an editor and the checker would
// disagree about the same file.
func TestSchemaPatternsAgreeWithTheValidatorsInCode(t *testing.T) {
	schema := manifest.Schema()
	pattern := func(at ...string) *regexp.Regexp {
		return regexp.MustCompile(dig(t, schema, append(at, "pattern")...).(string))
	}
	agree := func(name string, re *regexp.Regexp, samples []string, accepted func(string) bool) {
		for _, s := range samples {
			if re.MatchString(s) != accepted(s) {
				t.Errorf("%s: schema and validator disagree about %q", name, s)
			}
		}
	}
	ids := []string{"acme-app", "a", "0x.y_z", strings.Repeat("a", 63), "Acme", "-a", "", "a b", strings.Repeat("a", 64)}
	agree("factory_id", pattern("properties", "factory_id"), ids, func(s string) bool { return core.FactoryID(s).Validate() == nil })
	agree("deployment_id", pattern("properties", "deployment_id"), ids, func(s string) bool { return core.DeploymentID(s).Validate() == nil })

	digests := []string{"sha256:" + strings.Repeat("a", 64), "sha512:" + strings.Repeat("0", 128), "latest", "sha256:abc", "SHA256:" + strings.Repeat("a", 64), ""}
	agree("digest", pattern("$defs", "Supervisor", "properties", "digest"), digests, func(s string) bool { return core.Digest(s).Validate() == nil })

	hosts := []string{"api.github.com", "*.githubusercontent.com", "ghcr.io", "*", "*.", "https://github.com", "GitHub.com", "a..b", ""}
	agree("egress host", pattern("$defs", "EgressPolicy", "properties", "allow", "items"), hosts, func(s string) bool {
		return core.EgressPolicy{Mode: core.EgressAllowlist, Allow: []string{s}}.Validate() == nil
	})

	// A duration is validated in the form it is written back in.
	spans := pattern("$defs", "Limits", "properties", "run_timeout")
	for _, d := range []time.Duration{0, time.Nanosecond, 1500 * time.Microsecond, 90 * time.Second, 72 * time.Hour, 1<<63 - 1} {
		text, err := manifest.Duration(d).MarshalText()
		if err != nil || !spans.Match(text) {
			t.Errorf("duration %v is written %q, which the schema rejects", d, text)
		}
	}
	for _, s := range []string{"soon", "-5m", "10", "", "5 m"} {
		if spans.MatchString(s) {
			t.Errorf("the schema accepts %q as a duration", s)
		}
	}
}
