package appbundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

func TestThePairObserverMeasuresTheStoredBundle(t *testing.T) {
	t.Parallel()
	s, digest, data := storedBundle(t)
	image := "wordpress:7.0@sha256:" + strings.Repeat("a", 64)
	a := deploy.Artifact{Unit: "app", Image: image, Bundle: digest, Digest: deploy.PairDigest(image, digest)}
	o := PairObserver{Store: s}
	got, err := o.ObserveArtifact(context.Background(), a)
	if err != nil || got != a.Digest {
		t.Fatalf("observed %s, %v; want %s", got, err, a.Digest)
	}
	// Bytes changed on disk are observed as another release.
	if err := os.WriteFile(filepath.Join(s.Dir, strings.TrimPrefix(digest, "sha256:")+".tar"), append(data, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := o.ObserveArtifact(context.Background(), a); err != nil || got == a.Digest {
		t.Fatalf("a changed bundle was observed as the signed one: %s, %v", got, err)
	}
}

func TestThePairObserverObservesNothingItDoesNotHold(t *testing.T) {
	t.Parallel()
	o := PairObserver{Store: Store{Dir: t.TempDir()}}
	missing := deploy.Artifact{Unit: "app", Image: "x@sha256:" + strings.Repeat("a", 64), Bundle: "sha256:" + strings.Repeat("b", 64)}
	if _, err := o.ObserveArtifact(context.Background(), missing); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("an unstored bundle: %v", err)
	}
	if _, err := o.ObserveArtifact(context.Background(), deploy.Artifact{Unit: "app", Image: missing.Image}); !core.HasCode(err, core.CodeNotFound) {
		t.Fatalf("an image-only artifact: %v", err)
	}
}
