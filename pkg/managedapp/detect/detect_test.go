package detect

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// The trial shop adopts as a WordPress Spec without any WordPress knowledge
// outside its detector: runtime, data and the database come from Compose.
func TestTheWooCommerceExampleAdoptsAsAManagedApplication(t *testing.T) {
	t.Parallel()
	r, err := Adopt(filepath.Join(repoRoot(t), "examples", "woocommerce-app"), "shop", Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Spec
	if s.Stack != "wordpress" || s.Runtime.Port != 80 || s.Runtime.User != "33:33" || s.Bundle.Lock != "content.lock" {
		t.Fatalf("runtime or bundle: %+v %+v", s.Runtime, s.Bundle)
	}
	if s.Runtime.Secrets["WORDPRESS_DB_PASSWORD"] != "db_password" || s.Runtime.Secrets["WORDPRESS_SECRET"] != "wp_secret" {
		t.Fatalf("secrets: %v", s.Runtime.Secrets)
	}
	if len(s.Data) != 1 || s.Data[0].Name != "uploads" || s.Data[0].Owner != "33:33" {
		t.Fatalf("data: %+v", s.Data)
	}
	if m := s.BundleMountpoints(); len(m) != 1 || m[0] != "wp-content/uploads" {
		t.Fatalf("mountpoints: %v", m)
	}
	if len(s.Dependencies) != 1 || s.Dependencies[0].Name != "db" || len(s.Dependencies[0].Backup) == 0 {
		t.Fatalf("dependencies: %+v", s.Dependencies)
	}
	for _, b := range s.Dependencies[0].Backup {
		if strings.Contains(b, " -p") {
			t.Fatal("the backup passes a password on its command line")
		}
	}
	if !slices.Contains(s.ProtectedPaths(), "plugins/sisf-sandbox-pay/*") || !slices.Contains(s.ProtectedPaths(), "content.lock") {
		t.Fatalf("protected: %v", s.ProtectedPaths())
	}
	var purchase *managedapp.Check
	for i := range s.Checks {
		if s.Checks[i].Name == "purchase" {
			purchase = &s.Checks[i]
		}
	}
	if purchase == nil || !purchase.Writes {
		t.Fatalf("a shop needs a purchase check that is marked as writing: %+v", s.Checks)
	}
	if _, err := managedapp.Marshal(s, "test"); err != nil {
		t.Fatal(err)
	}
}

// The second stack proves the contract is general: a Go program adopts with
// a program bundle and no lockfile.
func TestTheHelloAppAdoptsAsAGoProgram(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	r, err := Adopt(filepath.Join(root, "examples", "hello-app"), "hello", Options{
		Root: root, Port: 7461, Env: map[string]string{"HELLO_ADDR": "0.0.0.0:7461", "HELLO_DATA_DIR": "/data"},
		Data: []managedapp.Volume{{Name: "hello-data", Target: "/data"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Spec
	if s.Stack != "go-program" || s.Bundle.Program != "./examples/hello-app" || s.Bundle.Lock != "" || s.Runtime.Port != 7461 {
		t.Fatalf("%+v %+v", s.Runtime, s.Bundle)
	}
	if s.Runtime.Command[0] != "/app/hello-app" || s.Data[0].Owner != "10001:10001" {
		t.Fatalf("%+v %+v", s.Runtime, s.Data)
	}
}

func writeApp(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const pinned = "@sha256:b427cec767f5de2aa649390cb8805aa1fe320e1e0d57fc1f467754edb6cc0a49"

// Adoption never copies a secret value: a password in plain environment is
// refused, and the error names the variable without its value.
func TestAPlainSecretIsRefusedWithoutPrintingIt(t *testing.T) {
	t.Parallel()
	dir := writeApp(t, map[string]string{
		"content.lock": "core wordpress 7.0.4 " + strings.Repeat("a", 64) + "\n",
		"compose.yaml": "services:\n  web:\n    image: wordpress:7" + pinned + "\n    ports: [\"8080:80\"]\n    environment:\n      WORDPRESS_DB_PASSWORD: hunter2-value\n",
	})
	_, err := Adopt(dir, "shop", Options{})
	if err == nil || !strings.Contains(err.Error(), "WORDPRESS_DB_PASSWORD") {
		t.Fatalf("want a refusal naming the variable, got %v", err)
	}
	if strings.Contains(err.Error(), "hunter2-value") {
		t.Fatal("the refusal printed the secret")
	}
}

func TestAdoptionRefusesWhatItCannotDescribeSafely(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"wordpress without a lockfile": {map[string]string{
			"compose.yaml": "services:\n  web:\n    image: wordpress:7" + pinned + "\n    ports: [\"8080:80\"]\n",
		}, "lockfile"},
		"unpinned image": {map[string]string{
			"content.lock": "core wordpress 7.0.4 " + strings.Repeat("a", 64) + "\n",
			"compose.yaml": "services:\n  web:\n    image: wordpress:latest\n    ports: [\"8080:80\"]\n",
		}, "not pinned"},
		"two published services": {map[string]string{
			"compose.yaml": "services:\n  a:\n    image: x" + pinned + "\n    ports: [\"1:1\"]\n  b:\n    image: y" + pinned + "\n    ports: [\"2:2\"]\n",
		}, "exactly one service"},
		"unknown stack": {map[string]string{
			"compose.yaml": "services:\n  a:\n    image: nginx" + pinned + "\n    ports: [\"1:1\"]\n",
		}, "no known stack"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Adopt(writeApp(t, c.files), "shop", Options{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}
