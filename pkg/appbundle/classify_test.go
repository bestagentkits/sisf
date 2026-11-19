package appbundle

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

const otherSHA = "a5feaa9988f31cea6b052865d1c3ee080daa04ec23ffab7266a63f448e9c90d1"

func mustLock(t *testing.T, lines ...string) Lock {
	t.Helper()
	l, err := ParseLock(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestClassify(t *testing.T) {
	t.Parallel()
	base := []string{"core wordpress 7.0.4 " + sha, "plugin shop 2.1.3 " + sha}
	for name, tc := range map[string]struct {
		candidate []string
		want      []Change
	}{
		"unchanged":              {base, nil},
		"patch":                  {[]string{base[0], "plugin shop 2.1.4 " + otherSHA}, []Change{{"plugin", "shop", "2.1.3", "2.1.4", managedapp.LockPatch}}},
		"minor":                  {[]string{base[0], "plugin shop 2.2.0 " + otherSHA}, []Change{{"plugin", "shop", "2.1.3", "2.2.0", managedapp.LockMinor}}},
		"major":                  {[]string{base[0], "plugin shop 3.0.0 " + otherSHA}, []Change{{"plugin", "shop", "2.1.3", "3.0.0", managedapp.LockMajor}}},
		"missing parts are zero": {[]string{base[0], "plugin shop 2.1 " + otherSHA}, []Change{{"plugin", "shop", "2.1.3", "2.1", managedapp.LockPatch}}},
		"patch downgrade":        {[]string{base[0], "plugin shop 2.1.2 " + otherSHA}, []Change{{"plugin", "shop", "2.1.3", "2.1.2", managedapp.LockPatch}}},
		"minor downgrade":        {[]string{base[0], "plugin shop 2.0.9 " + otherSHA}, []Change{{"plugin", "shop", "2.1.3", "2.0.9", managedapp.LockMinor}}},
		"major downgrade":        {[]string{base[0], "plugin shop 1.9.9 " + otherSHA}, []Change{{"plugin", "shop", "2.1.3", "1.9.9", managedapp.LockMajor}}},
		"pre-release":            {[]string{base[0], "plugin shop 2.1.4-beta1 " + otherSHA}, []Change{{"plugin", "shop", "2.1.3", "2.1.4-beta1", managedapp.LockMajor}}},
		"four parts":             {[]string{base[0], "plugin shop 2.1.3.1 " + otherSHA}, []Change{{"plugin", "shop", "2.1.3", "2.1.3.1", managedapp.LockMajor}}},
		"same version new sha":   {[]string{base[0], "plugin shop 2.1.3 " + otherSHA}, []Change{{"plugin", "shop", "2.1.3", "2.1.3", managedapp.LockMajor}}},
		"equal but respelled":    {[]string{base[0], "plugin shop 02.1.3 " + sha}, []Change{{"plugin", "shop", "2.1.3", "02.1.3", managedapp.LockMajor}}},
		"added":                  {[]string{base[0], base[1], "theme storefront 4.0 " + sha}, []Change{{"theme", "storefront", "", "4.0", managedapp.LockAdded}}},
		"removed":                {[]string{base[0]}, []Change{{"plugin", "shop", "2.1.3", "", managedapp.LockRemoved}}},
		"core patch":             {[]string{"core wordpress 7.0.5 " + otherSHA, base[1]}, []Change{{"core", "wordpress", "7.0.4", "7.0.5", managedapp.LockCore}}},
		"core same version sha":  {[]string{"core wordpress 7.0.4 " + otherSHA, base[1]}, []Change{{"core", "wordpress", "7.0.4", "7.0.4", managedapp.LockCore}}},
		"core removed":           {[]string{base[1]}, []Change{{"core", "wordpress", "7.0.4", "", managedapp.LockCore}}},
		"several, in order": {[]string{"plugin zeta 1.0 " + sha, "plugin shop 2.1.4 " + otherSHA, "core wordpress 7.1.0 " + otherSHA}, []Change{
			{"core", "wordpress", "7.0.4", "7.1.0", managedapp.LockCore},
			{"plugin", "shop", "2.1.3", "2.1.4", managedapp.LockPatch},
			{"plugin", "zeta", "", "1.0", managedapp.LockAdded},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := Classify(mustLock(t, base...), mustLock(t, tc.candidate...))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestClassifyCoreAddedIsCore(t *testing.T) {
	t.Parallel()
	got := Classify(mustLock(t, "plugin shop 1.0 "+sha), mustLock(t, "plugin shop 1.0 "+sha, "core wordpress 7.0 "+sha))
	want := []Change{{"core", "wordpress", "", "7.0", managedapp.LockCore}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
