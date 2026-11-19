package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/managedapp"
	"github.com/bestagentkits/sisf/pkg/managedapp/detect"
)

// shopWithCachedPlugin writes a WordPress app whose lockfile pins one
// plugin already in a verified cache, so no download happens.
func shopWithCachedPlugin(t *testing.T) (spec, cache string) {
	t.Helper()
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, err := zw.Create("shop/shop.php")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("<?php // shop"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(zb.Bytes())
	sha := hex.EncodeToString(sum[:])
	cache = t.TempDir()
	if err := os.WriteFile(filepath.Join(cache, sha+".zip"), zb.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	app := t.TempDir()
	files := map[string]string{
		"content.lock":  "plugin shop 1.0.0 " + sha + "\n",
		"compose.yaml":  "services:\n  web:\n    image: wordpress:7@sha256:" + strings.Repeat("b", 64) + "\n    ports: [\"8080:80\"]\n",
		"wp-config.php": "<?php // config",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(app, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := detect.Adopt(app, "shop", detect.Options{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := managedapp.Marshal(res.Spec, "test")
	if err != nil {
		t.Fatal(err)
	}
	spec = filepath.Join(app, managedapp.FileName)
	if err := os.WriteFile(spec, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return spec, cache
}

func TestAppBundleWritesTheSameDigestTwice(t *testing.T) {
	t.Parallel()
	spec, cache := shopWithCachedPlugin(t)
	out := t.TempDir()
	var digests []string
	for _, name := range []string{"a.tar", "b.tar"} {
		var buf bytes.Buffer
		err := runAppBundle(context.Background(), &Env{Stdout: &buf, Stderr: &buf},
			[]string{"--spec", spec, "--cache", cache, "--out", filepath.Join(out, name)})
		if err != nil {
			t.Fatal(err)
		}
		digests = append(digests, strings.Fields(buf.String())[0])
	}
	if !strings.HasPrefix(digests[0], "sha256:") || digests[0] != digests[1] {
		t.Fatalf("digests %v", digests)
	}
}

func TestAppBundleRefusesWhatItCannotBuild(t *testing.T) {
	t.Parallel()
	spec, cache := shopWithCachedPlugin(t)
	var buf bytes.Buffer
	env := &Env{Stdout: &buf, Stderr: &buf}
	if err := runAppBundle(context.Background(), env, []string{"--spec", spec, "--cache", cache}); err == nil || !strings.Contains(err.Error(), "--out") {
		t.Fatalf("no --out: %v", err)
	}
	// A program bundle is built by the factory's sandbox, never here.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	res, err := detect.Adopt(filepath.Join(root, "examples", "hello-app"), "hello", detect.Options{Root: root, Port: 7461})
	if err != nil {
		t.Fatal(err)
	}
	data, err := managedapp.Marshal(res.Spec, "test")
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), managedapp.FileName)
	if err := os.WriteFile(program, data, 0o644); err != nil {
		t.Fatal(err)
	}
	err = runAppBundle(context.Background(), env, []string{"--spec", program, "--cache", cache, "--out", filepath.Join(t.TempDir(), "x.tar")})
	if err == nil || !strings.Contains(err.Error(), "program") {
		t.Fatalf("program bundle: %v", err)
	}
}
