package managedapp

import (
	"reflect"
	"strings"
	"testing"
)

func shopSpec(t *testing.T) *Spec {
	t.Helper()
	s, err := Load("../../examples/woocommerce-app/" + FileName)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var shopBinding = Binding{Volumes: map[string]string{"uploads": "shop-uploads"}, Secrets: "shop-secrets"}

// The revision of the trial shop: its pinned image, port, user, sorted
// environment, bundle mount, the uploads volume writable and the secrets
// volume read-only at SecretsDir.
func TestTheShopsRevisionComesFromItsSpec(t *testing.T) {
	t.Parallel()
	spec := shopSpec(t)
	rev, err := spec.Revision(shopBinding)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Image != spec.Runtime.Image || rev.Port != 80 || rev.User != "33:33" || rev.BundleTarget != "/var/www/html" {
		t.Fatalf("%+v", rev)
	}
	if !reflect.DeepEqual(rev.Env, spec.Runtime.RevisionEnv()) {
		t.Fatalf("env %v", rev.Env)
	}
	want := []Mount{
		{Name: "shop-uploads", Target: "/var/www/html/wp-content/uploads"},
		{Name: "shop-secrets", Target: SecretsDir, ReadOnly: true},
	}
	if !reflect.DeepEqual(rev.Mounts, want) {
		t.Fatalf("mounts %+v", rev.Mounts)
	}
}

// An install that does not bind the Spec's volumes exactly is refused, so
// a revision never starts without its data or with a volume nobody named.
func TestARevisionNeedsEveryVolumeBoundExactly(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		b    Binding
		want string
	}{
		"no uploads":          {Binding{Secrets: "shop-secrets"}, `data volume "uploads" is not bound`},
		"an extra volume":     {Binding{Volumes: map[string]string{"uploads": "u", "cache": "c"}, Secrets: "s"}, `"cache" is not a data volume`},
		"no secrets volume":   {Binding{Volumes: map[string]string{"uploads": "u"}}, "needs a secrets volume"},
		"a malformed name":    {Binding{Volumes: map[string]string{"uploads": "../u"}, Secrets: "s"}, "not a volume name"},
		"one volume twice":    {Binding{Volumes: map[string]string{"uploads": "same"}, Secrets: "same"}, "bound twice"},
		"a bundle-like name":  {Binding{Volumes: map[string]string{"uploads": "x-bundle-abc"}, Secrets: "s"}, "bundle volume"},
		"secrets without any": {Binding{Volumes: map[string]string{"uploads": "u"}, Secrets: "s"}, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spec := shopSpec(t)
			if name == "secrets without any" {
				spec.Runtime.Secrets = nil
				c.want = "has no secrets"
			}
			_, err := spec.Revision(c.b)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error mentioning %q, got %v", c.want, err)
			}
		})
	}
}

// What the compose backend cannot honour is refused rather than dropped.
func TestARevisionTheBackendCannotRunIsRefused(t *testing.T) {
	t.Parallel()
	spec := shopSpec(t)
	spec.Runtime.Command = []string{"apache2-foreground"}
	if _, err := spec.Revision(shopBinding); err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("a runtime command: %v", err)
	}
	spec = shopSpec(t)
	spec.Runtime.HealthPath = "/status"
	if _, err := spec.Revision(shopBinding); err == nil || !strings.Contains(err.Error(), HealthPath) {
		t.Fatalf("a health path other than %s: %v", HealthPath, err)
	}
}
