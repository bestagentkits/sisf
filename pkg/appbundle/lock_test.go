package appbundle

import (
	"os"
	"strings"
	"testing"
)

const sha = "f0885ad10c35ac87e70517bc5789275cd54ccac052222a58acfb7b9f870082b0"

func TestParseLockReadsTheExampleShop(t *testing.T) {
	t.Parallel()
	f, err := os.Open("../../examples/woocommerce-app/content.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	lock, err := ParseLock(f)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range lock.Entries {
		got = append(got, e.URL())
	}
	want := []string{
		"https://downloads.wordpress.org/release/wordpress-7.0.4.zip",
		"https://downloads.wordpress.org/plugin/contact-form-7.6.1.3.zip",
		"https://downloads.wordpress.org/plugin/woocommerce.11.2.1.zip",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("URLs in canonical order:\n got %v\nwant %v", got, want)
	}
}

func TestEntryURLPerKind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		e    Entry
		want string
	}{
		{Entry{Kind: "core", Slug: "wordpress", Version: "6.8"}, "https://downloads.wordpress.org/release/wordpress-6.8.zip"},
		{Entry{Kind: "plugin", Slug: "akismet", Version: "5.3"}, "https://downloads.wordpress.org/plugin/akismet.5.3.zip"},
		{Entry{Kind: "theme", Slug: "storefront", Version: "4.6.1"}, "https://downloads.wordpress.org/theme/storefront.4.6.1.zip"},
	} {
		if got := tc.e.URL(); got != tc.want {
			t.Errorf("%s URL = %s, want %s", tc.e.Kind, got, tc.want)
		}
	}
}

func TestParseLockRefusesMalformedLines(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ text, want string }{
		"unknown kind":     {"module x 1.0 " + sha, "unknown kind"},
		"uppercase slug":   {"plugin Shop 1.0 " + sha, "slug"},
		"slug with slash":  {"plugin a/b 1.0 " + sha, "slug"},
		"slug with dot":    {"plugin a.b 1.0 " + sha, "slug"},
		"leading dash":     {"plugin -a 1.0 " + sha, "slug"},
		"version slash":    {"plugin a 1.0/../x " + sha, "version"},
		"version query":    {"plugin a 1.0?x=1 " + sha, "version"},
		"empty-ish ver":    {"plugin a . " + sha, "version"},
		"short sha":        {"plugin a 1.0 abc", "sha256"},
		"uppercase sha":    {"plugin a 1.0 " + strings.ToUpper(sha), "sha256"},
		"too few fields":   {"plugin a " + sha, "fields"},
		"trailing comment": {"plugin a 1.0 " + sha + " # note", "fields"},
		"duplicate":        {"plugin a 1.0 " + sha + "\nplugin a 2.0 " + sha, "twice"},
		"two cores":        {"core wordpress 1.0 " + sha + "\ncore other 1.0 " + sha, "more than one core"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseLock(strings.NewReader("# header\n\n" + tc.text + "\n"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestParseLockAllowsSameSlugAcrossKinds(t *testing.T) {
	t.Parallel()
	lock, err := ParseLock(strings.NewReader("plugin a 1.0 " + sha + "\ntheme a 1.0 " + sha + "\n"))
	if err != nil || len(lock.Entries) != 2 {
		t.Fatalf("lock = %+v, err = %v", lock, err)
	}
}

func TestParseLockRefusesOversizedFile(t *testing.T) {
	t.Parallel()
	_, err := ParseLock(strings.NewReader(strings.Repeat("#", maxLockBytes+1)))
	if err == nil {
		t.Fatal("an oversized lockfile was accepted")
	}
}
