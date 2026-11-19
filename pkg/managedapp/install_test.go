package managedapp

import (
	"path/filepath"
	"testing"
)

// The Spec of an install is the Owner's file at an absolute path; a
// relative path, which would resolve inside whatever directory the
// process runs in, is refused.
func TestAnInstallLoadsTheOwnersSpecByAbsolutePath(t *testing.T) {
	t.Parallel()
	var b Binding
	for _, v := range []string{"uploads=shop-uploads"} {
		if err := b.BindVolume(v); err != nil {
			t.Fatal(err)
		}
	}
	b.Secrets = "shop-secrets"
	abs, err := filepath.Abs("../../examples/woocommerce-app/" + FileName)
	if err != nil {
		t.Fatal(err)
	}
	in, err := LoadInstall(abs, b)
	if err != nil || in.Revision.Mounts[0].Name != "shop-uploads" {
		t.Fatalf("%+v, %v", in, err)
	}
	if _, err := LoadInstall("../../examples/woocommerce-app/"+FileName, b); err == nil {
		t.Fatal("a relative Spec path was loaded")
	}
}

func TestAVolumeBindingIsNameEqualsVolumeOnce(t *testing.T) {
	t.Parallel()
	var b Binding
	for _, bad := range []string{"uploads", "=v", "uploads="} {
		if err := b.BindVolume(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := b.BindVolume("uploads=a"); err != nil {
		t.Fatal(err)
	}
	if err := b.BindVolume("uploads=b"); err == nil {
		t.Fatal("a data volume was bound twice")
	}
}
