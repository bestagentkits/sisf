package compose_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/adapters/deploy/compose"
	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/managedapp"
)

func shopRevision(t *testing.T) managedapp.RevisionConfig {
	t.Helper()
	spec, err := managedapp.Load("../../../examples/woocommerce-app/" + managedapp.FileName)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := spec.Revision(managedapp.Binding{Volumes: map[string]string{"uploads": "acme-app_uploads"}, Secrets: "acme-app_secrets"})
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

// shopWorld runs the shop's revision through a deploy filter that admits
// admitted, both configured from a revision of the shop's Spec.
func shopWorld(t *testing.T, started, admitted managedapp.RevisionConfig) *world {
	t.Helper()
	filter := &dockerfilter.Config{DeployNetwork: "acme-app_default"}
	filter.AdmitRevision(admitted)
	return newWorld(t, worldOptions{
		cfg:    func(c *compose.Config) { c.UseRevision(started); c.BundleSource = newBundles() },
		filter: filter,
	})
}

func shopArtifact(rev managedapp.RevisionConfig) deploy.Artifact {
	a := artifact(digestA)
	a.Image, a.Bundle = rev.Image, bundleA
	a.Digest = deploy.PairDigest(a.Image, a.Bundle)
	return a
}

// The backend and the filter configured from one derivation of the Spec
// agree: the shop's revision is staged through the filter.
func TestTheShopsRevisionPassesAFilterConfiguredFromTheSameSpec(t *testing.T) {
	t.Parallel()
	rev := shopRevision(t)
	w := shopWorld(t, rev, rev)
	a := shopArtifact(rev)
	if r, err := w.backend.Stage(context.Background(), a); err != nil || !w.running(name(a.Digest)) {
		t.Fatalf("Stage: %+v, %v", r, err)
	}
}

// A revision that differs from what the filter admits by one entry is
// refused: a writable secrets volume, an extra environment entry, another
// volume.
func TestARevisionThatDriftsFromTheFilterIsRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*managedapp.RevisionConfig){
		"a writable secrets volume": func(r *managedapp.RevisionConfig) { r.Mounts[1].ReadOnly = false },
		"an extra environment entry": func(r *managedapp.RevisionConfig) {
			r.Env = append(append([]string{}, r.Env...), "WP_DEBUG=1")
		},
		"another volume": func(r *managedapp.RevisionConfig) { r.Mounts[0].Name = "acme-app_other" },
	}
	for name, drift := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			admitted, started := shopRevision(t), shopRevision(t)
			drift(&started)
			w := shopWorld(t, started, admitted)
			if _, err := w.backend.Stage(context.Background(), shopArtifact(started)); err == nil || !strings.Contains(err.Error(), "refused by the sisf docker filter") {
				t.Fatalf("want the filter to refuse the drifted revision, got %v", err)
			}
		})
	}
}
