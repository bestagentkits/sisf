package install

import (
	"strings"
	"testing"
)

// Compose merges every document of a file and drops a key tagged !reset or
// replaces one tagged !override, while the checker reads the first document
// as plain YAML. A file whose bytes say more than the checker reads is
// refused, so the file that runs is the file that was checked.
func TestTheCheckerRefusesYAMLThatComposeReadsDifferently(t *testing.T) {
	t.Parallel()
	in, out := renderCompose(t, nil)
	file := string(out)
	replaced := func(old, replacement string) []byte {
		t.Helper()
		if !strings.Contains(file, old) {
			t.Fatalf("the generated file has no %q", old)
		}
		return []byte(strings.Replace(file, old, replacement, 1))
	}
	cases := map[string][]byte{
		"a second document that makes a service privileged": []byte(file + "\n---\nservices:\n  app:\n    privileged: true\n"),
		"a second document that only names the project":     []byte(file + "\n---\nname: other\n"),
		"an empty second document":                          []byte(file + "\n---\n"),
		"the hardening block resets security_opt":           replaced(`security_opt: ["no-new-privileges:true"]`+"\n  cap_drop", `security_opt: !reset ["no-new-privileges:true"]`+"\n  cap_drop"),
		"the hardening block resets cap_drop":               replaced("cap_drop: [ALL]", "cap_drop: !reset [ALL]"),
		"a service overrides its networks":                  replaced("networks: [app]", "networks: !override [app]"),
		"a value carries a local tag":                       replaced("cap_drop: [ALL]", "cap_drop: [!custom ALL]"),
		"a value carries a binary tag":                      replaced("cap_drop: [ALL]", "cap_drop: [!!binary QUxM]"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := CheckCompose(in.Layout, data); err == nil {
				t.Fatal("the file must be refused as unreadable")
			}
		})
	}

	// Standard tags written out, and the merge the template uses, are
	// plain YAML that Compose reads as the checker does.
	explicit := replaced("cap_drop: [ALL]", "cap_drop: !!seq [!!str ALL]")
	if vs, err := CheckCompose(in.Layout, explicit); err != nil || len(vs) != 0 {
		t.Fatalf("standard tags must pass: violations [%s] (%v)", violationRules(vs), err)
	}
}
