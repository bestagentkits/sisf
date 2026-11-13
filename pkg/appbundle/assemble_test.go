package appbundle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func assemble(t *testing.T, s *scenario) Result {
	t.Helper()
	res, err := Assemble(context.Background(), s.in)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAssembleIsReproducible(t *testing.T) {
	t.Parallel()
	a, b := newScenario(t), newScenario(t)
	// The same content, with the lockfile's lines in another order.
	b.lock[coreLine], b.lock[themeLine] = b.lock[themeLine], b.lock[coreLine]
	b.writeLock(t)
	ra, rb := assemble(t, a), assemble(t, b)
	if ra.Digest != rb.Digest || ra.Files != rb.Files || ra.Bytes != rb.Bytes {
		t.Fatalf("two builds of the same content differ: %+v vs %+v", ra, rb)
	}
	again := assemble(t, a)
	if again.Digest != ra.Digest {
		t.Fatalf("a rebuild from cache changed the digest: %s vs %s", again.Digest, ra.Digest)
	}
	if err := CheckDigest(a.in.OutputPath, ra.Digest); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(a.in.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if got, err := Verify(f, a.in.Limits); err != nil || got != ra.Digest {
		t.Fatalf("Verify = %s, %v; want %s", got, err, ra.Digest)
	}
}

func TestAssembleTarIsNormalised(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	res := assemble(t, s)
	want := map[string]int64{
		"index.php": 0o644, "wp-admin/": 0o755, "bin/tool.sh": 0o755, longName: 0o644,
		"wp-content/plugins/shop/shop.php":       0o644,
		"wp-content/themes/storefront/style.css": 0o644,
		"wp-content/plugins/custom/custom.php":   0o644,
		"wp-config.php":                          0o644, ".htaccess": 0o644,
		"wp-includes/blocks/archives/":    0o755,
		"wp-includes/blocks/archives.php": 0o644,
	}
	pruned := map[string]bool{"wp-content/plugins/akismet/": true, "wp-content/plugins/akismet/akismet.php": true, "wp-content/plugins/hello.php": true}
	files, prev := 0, ""
	for _, h := range tarHeaders(t, s.in.OutputPath) {
		// Sorted bytewise by path; a directory's trailing slash is not part
		// of its path ("blocks/archives/" sorts before "blocks/archives.php").
		if p := strings.TrimSuffix(h.Name, "/"); p <= prev {
			t.Errorf("%q is not after %q", p, prev)
		} else {
			prev = p
		}
		if pruned[h.Name] {
			t.Errorf("pruned %q is in the bundle", h.Name)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || !h.ModTime.Equal(bundleTime) ||
			!h.AccessTime.IsZero() || !h.ChangeTime.IsZero() || h.Devmajor != 0 || h.Devminor != 0 {
			t.Errorf("%q header is not normalised: %+v", h.Name, h)
		}
		for k := range h.PAXRecords {
			if k != "path" {
				t.Errorf("%q has extended record %q", h.Name, k)
			}
		}
		if m, ok := want[h.Name]; ok && h.Mode != m {
			t.Errorf("%q mode %o, want %o", h.Name, h.Mode, m)
		}
		delete(want, h.Name)
		if h.Typeflag == 0 || h.Typeflag == '0' {
			files++
		}
	}
	if len(want) != 0 {
		t.Errorf("missing from the bundle: %v", want)
	}
	if files != res.Files {
		t.Errorf("Result.Files = %d, tar has %d", res.Files, files)
	}
}

func TestAssembleUsesOnlyAVerifiedCache(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	first := assemble(t, s)
	calls := s.fetcher.calls
	assemble(t, s)
	if s.fetcher.calls != calls {
		t.Fatalf("a full cache was not used: %d fetches, want %d", s.fetcher.calls, calls)
	}
	// A tampered cache entry is not trusted: it is fetched again.
	entries, err := os.ReadDir(s.in.CacheDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("cache: %v, %d entries", err, len(entries))
	}
	if err := os.WriteFile(filepath.Join(s.in.CacheDir, entries[0].Name()), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := assemble(t, s); got.Digest != first.Digest || s.fetcher.calls != calls+1 {
		t.Fatalf("after tampering: digest %s (want %s), %d fetches (want %d)", got.Digest, first.Digest, s.fetcher.calls, calls+1)
	}
}

func TestAssembleRefusesShaMismatch(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	evil := makeZip(t, zipEntry{name: "shop/shop.php", body: "<?php // swapped"})
	s.fetcher.files["https://downloads.wordpress.org/plugin/shop.2.1.0.zip"] = evil
	assertRefused(t, s, "sha256")
	pinned := strings.Fields(s.lock[pluginLine])[3]
	entries, err := os.ReadDir(s.in.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == pinned+".zip" || strings.HasPrefix(e.Name(), ".fetch-") {
			t.Fatalf("a mismatching download was kept in the cache as %s", e.Name())
		}
	}
}

func TestAssemblePinnedPluginReplacesTheCoreCopy(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	s.in.Bundle.Prune = nil
	akismet := makeZip(t, zipEntry{name: "akismet/main.php", body: "<?php // pinned"})
	s.lock = append(s.lock, lockLine("plugin", "akismet", "5.0", akismet))
	s.fetcher.files["https://downloads.wordpress.org/plugin/akismet.5.0.zip"] = akismet
	s.writeLock(t)
	assemble(t, s)
	names := map[string]bool{}
	for _, h := range tarHeaders(t, s.in.OutputPath) {
		names[h.Name] = true
	}
	if !names["wp-content/plugins/akismet/main.php"] || names["wp-content/plugins/akismet/akismet.php"] {
		t.Fatalf("the pinned plugin did not replace the core's copy: %v", names)
	}
}

func TestAssembleHonoursCancellation(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	if _, err := Assemble(ctx, s.in); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
}
