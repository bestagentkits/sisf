package appbundle

import (
	"archive/zip"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// assertRefused checks that assembling s fails mentioning want and leaves
// neither an output tar nor a staging tree behind.
func assertRefused(t *testing.T, s *scenario, want string) {
	t.Helper()
	_ = refusal(t, s, want)
}

// refusal is assertRefused returning the error, to check its class.
func refusal(t *testing.T, s *scenario, want string) error {
	t.Helper()
	_, err := Assemble(context.Background(), s.in)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want a refusal mentioning %q", err, want)
	}
	if _, err := os.Stat(s.in.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused build left %s: %v", s.in.OutputPath, err)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(s.in.OutputPath), ".bundle-*"))
	if len(left) != 0 {
		t.Fatalf("a refused build left %v", left)
	}
	return err
}

func TestAssembleRefusesMaliciousArchiveEntries(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		line    int
		entries []zipEntry
		want    string
	}{
		"climbs inside":     {pluginLine, []zipEntry{{name: "shop/../../evil.php"}}, "climbs"},
		"climbs at top":     {pluginLine, []zipEntry{{name: "../evil.php"}}, "climbs"},
		"absolute":          {pluginLine, []zipEntry{{name: "/etc/evil.php"}}, "absolute"},
		"backslash":         {pluginLine, []zipEntry{{name: "shop\\..\\evil.php"}}, "backslash"},
		"unclean":           {pluginLine, []zipEntry{{name: "shop/./evil.php"}}, "not a clean path"},
		"symlink":           {pluginLine, []zipEntry{{name: "shop/link", body: "/etc/passwd", mode: fs.ModeSymlink | 0o777}}, "not a plain file"},
		"fifo":              {pluginLine, []zipEntry{{name: "shop/fifo", mode: fs.ModeNamedPipe | 0o644}}, "not a plain file"},
		"device":            {pluginLine, []zipEntry{{name: "shop/dev", mode: fs.ModeDevice | fs.ModeCharDevice | 0o644}}, "not a plain file"},
		"dir without slash": {pluginLine, []zipEntry{{name: "shop/d", mode: fs.ModeDir | 0o755}}, "not a plain file"},
		"outside slug":      {pluginLine, []zipEntry{{name: "other/x.php"}}, "outside the archive's shop/"},
		"duplicate":         {pluginLine, []zipEntry{{name: "shop/a.php"}, {name: "shop/a.php"}}, "exists"},
		"core outside root": {coreLine, append(coreEntries(), zipEntry{name: "evil.php"}), "outside the archive's wordpress/"},
		"theme climbs":      {themeLine, []zipEntry{{name: "storefront/../../../x"}}, "climbs"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newScenario(t)
			s.setArchive(t, tc.line, makeZip(t, tc.entries...))
			assertRefused(t, s, tc.want)
		})
	}
}

func TestAssembleRefusesBombsAndLimits(t *testing.T) {
	t.Parallel()
	many := []zipEntry{}
	for i := range 40 {
		many = append(many, zipEntry{name: "storefront/f" + string(rune('a'+i%26)) + strings.Repeat("x", i) + ".css", body: strings.Repeat("z", 1000)})
	}
	for name, tc := range map[string]struct {
		archive func(t *testing.T) []byte
		limits  Limits
		want    string
		class   error
	}{
		// archive/zip itself stops a stream that outgrows its header; the
		// count while reading is the second line of defence.
		"understated size": {func(t *testing.T) []byte { return rawZip(t, "storefront/bomb.css", 256<<10, 10) }, defaultLimits(), "bomb.css", zip.ErrFormat},
		"declared too big": {func(t *testing.T) []byte { return rawZip(t, "storefront/big.css", 65<<10, 128<<10) }, defaultLimits(), "declares", errLimit},
		// Counted while reading: the budget left is smaller than the file.
		"total too big":    {func(t *testing.T) []byte { return makeZip(t, many...) }, Limits{MaxTotalBytes: 20 << 10, MaxFiles: 200, MaxFileBytes: 64 << 10}, "limit exceeded", errLimit},
		"too many entries": {func(t *testing.T) []byte { return makeZip(t, many...) }, Limits{MaxTotalBytes: 1 << 20, MaxFiles: 30, MaxFileBytes: 64 << 10}, "entries", errLimit},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newScenario(t)
			s.in.Limits = tc.limits
			s.setArchive(t, themeLine, tc.archive(t))
			if err := refusal(t, s, tc.want); !errors.Is(err, tc.class) {
				t.Fatalf("err = %v, want %v", err, tc.class)
			}
		})
	}
}

func TestAssembleRefusesLinksInTheRepository(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		setup func(t *testing.T, s *scenario)
		want  string
	}{
		"link inside a source dir": {func(t *testing.T, s *scenario) {
			symlink(t, "../../wp-config.php", s.in.RepoDir, "plugins/custom/link.php")
		}, "not a plain file"},
		"link pointing outside": {func(t *testing.T, s *scenario) {
			symlink(t, "/etc/passwd", s.in.RepoDir, "plugins/custom/passwd")
		}, "not a plain file"},
		"source is a link": {func(t *testing.T, s *scenario) {
			symlink(t, "plugins", s.in.RepoDir, "linked")
			s.in.Bundle.Sources = append(s.in.Bundle.Sources, managedapp.Source{From: "linked", To: "x"})
		}, "symbolic link"},
		"a parent is a link": {func(t *testing.T, s *scenario) {
			symlink(t, "plugins/custom", s.in.RepoDir, "alias")
			s.in.Bundle.Sources = append(s.in.Bundle.Sources, managedapp.Source{From: "alias/custom.php", To: "x.php"})
		}, "symbolic link"},
		"lockfile is a link": {func(t *testing.T, s *scenario) {
			symlink(t, "content.lock", s.in.RepoDir, "lock-link")
			s.in.Bundle.Lock = "lock-link"
		}, "symbolic link"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newScenario(t)
			tc.setup(t, s)
			assertRefused(t, s, tc.want)
		})
	}
}

func symlink(t *testing.T, target, root, name string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(name))); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestAssembleRefusesBadInput(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		edit func(in *Input)
		want string
	}{
		"zero limits":    {func(in *Input) { in.Limits = Limits{} }, "positive"},
		"no fetcher":     {func(in *Input) { in.Fetcher = nil }, "required"},
		"lock climbs":    {func(in *Input) { in.Bundle.Lock = "../content.lock" }, "climbs"},
		"prune climbs":   {func(in *Input) { in.Bundle.Prune = []string{"../../x"} }, "climbs"},
		"prune absolute": {func(in *Input) { in.Bundle.Prune = []string{"/etc"} }, "absolute"},
		"source to climbs": {func(in *Input) {
			in.Bundle.Sources = []managedapp.Source{{From: "htaccess", To: "../x"}}
		}, "climbs"},
		"source from absolute": {func(in *Input) {
			in.Bundle.Sources = []managedapp.Source{{From: "/etc/passwd", To: "x"}}
		}, "absolute"},
		"missing archive": {func(in *Input) { in.Fetcher = &fakeFetcher{} }, "no archive"},
		"lock is a dir":   {func(in *Input) { in.Bundle.Lock = "plugins" }, "not a regular file"},
		"oversized download": {func(in *Input) {
			f := in.Fetcher.(*fakeFetcher)
			f.files["https://downloads.wordpress.org/release/wordpress-7.0.4.zip"] = make([]byte, in.Limits.MaxTotalBytes+1)
		}, "larger than"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newScenario(t)
			tc.edit(&s.in)
			assertRefused(t, s, tc.want)
		})
	}
}
