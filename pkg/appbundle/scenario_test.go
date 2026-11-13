package appbundle

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// scenario is a small WordPress-like application ready to assemble: a
// core, a plugin and a theme served by a fake fetcher, and a repository
// with its own plugin and settings.
type scenario struct {
	in      Input
	fetcher *fakeFetcher
	lock    []string // lockfile lines, in file order
}

func newScenario(t *testing.T) *scenario {
	t.Helper()
	core := makeZip(t, coreEntries()...)
	plugin := makeZip(t, zipEntry{name: "shop/"}, zipEntry{name: "shop/shop.php", body: "<?php // shop"})
	theme := makeZip(t, zipEntry{name: "storefront/style.css", body: "/* storefront */"})
	f := &fakeFetcher{files: map[string][]byte{
		"https://downloads.wordpress.org/release/wordpress-7.0.4.zip": core,
		"https://downloads.wordpress.org/plugin/shop.2.1.0.zip":       plugin,
		"https://downloads.wordpress.org/theme/storefront.4.0.zip":    theme,
	}}
	work := t.TempDir()
	repo := filepath.Join(work, "repo")
	writeFiles(t, repo, map[string]string{
		"plugins/custom/custom.php": "<?php // custom",
		"wp-config.php":             "<?php // config",
		"htaccess":                  "# rules",
	})
	s := &scenario{
		fetcher: f,
		lock: []string{
			"# pinned content\n",
			lockLine("core", "wordpress", "7.0.4", core),
			lockLine("plugin", "shop", "2.1.0", plugin),
			"\n",
			lockLine("theme", "storefront", "4.0", theme),
		},
		in: Input{
			RepoDir: repo,
			Bundle: managedapp.Bundle{
				Lock:  "content.lock",
				Mount: "/var/www/html",
				Sources: []managedapp.Source{
					{From: "plugins", To: "wp-content/plugins"},
					{From: "wp-config.php", To: "wp-config.php"},
					{From: "htaccess", To: ".htaccess"},
				},
				Prune: []string{"wp-content/plugins/akismet", "wp-content/plugins/hello.php"},
			},
			Fetcher:    f,
			CacheDir:   filepath.Join(work, "cache"),
			OutputPath: filepath.Join(work, "bundle.tar"),
			Limits:     defaultLimits(),
		},
	}
	s.writeLock(t)
	return s
}

func (s *scenario) writeLock(t *testing.T) {
	t.Helper()
	writeFiles(t, s.in.RepoDir, map[string]string{"content.lock": strings.Join(s.lock, "")})
}

// Lock line indexes in a new scenario.
const (
	coreLine   = 1
	pluginLine = 2
	themeLine  = 4
)

// setArchive serves body for the entry on lock line i and re-pins it.
func (s *scenario) setArchive(t *testing.T, i int, body []byte) {
	t.Helper()
	var kind, slug, version, sum string
	if _, err := fmt.Sscan(s.lock[i], &kind, &slug, &version, &sum); err != nil {
		t.Fatal(err)
	}
	e := Entry{Kind: kind, Slug: slug, Version: version}
	s.fetcher.files[e.URL()] = body
	s.lock[i] = lockLine(kind, slug, version, body)
	s.writeLock(t)
}

// tarHeaders lists the headers of the tar at path.
func tarHeaders(t *testing.T, path string) []*tar.Header {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []*tar.Header
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h)
	}
}
