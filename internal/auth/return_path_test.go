package auth_test

import (
	"testing"

	"github.com/bestagentkits/sisf/internal/auth"
)

func TestLocalReturnPathKeepsOnlyPathsOnThisSite(t *testing.T) {
	t.Parallel()
	kept := []string{
		"/",
		"/approvals",
		"/approvals?status=pending&page=2",
		"/runs/r-1#graph",
		"/device?user_code=ABCD-EFGH",
		"/a/b/c",
		"/search?q=%2F%2Fevil.com",
	}
	for _, p := range kept {
		if got := auth.LocalReturnPath(p); got != p {
			t.Errorf("LocalReturnPath(%q) = %q, want it kept", p, got)
		}
	}
	refused := []string{
		"",
		"approvals",
		"//evil.com",
		"///evil.com",
		"/\\evil.com",
		"\\\\evil.com",
		"/\t/evil.com",
		"/\n/evil.com",
		"/\r/evil.com",
		"/\x00/evil.com",
		"/\x7f/evil.com",
		"/%09/evil.com",
		"/%0a/evil.com",
		"/%0D/evil.com",
		"/%00/evil.com",
		"/%2F/evil.com",
		"/%2f%2fevil.com",
		"/%5Cevil.com",
		"/%5cevil.com",
		"/%2e%2e//evil.com",
		"/.//evil.com",
		"/x/..//evil.com",
		"https://evil.com",
		"http:evil.com",
		"javascript:alert(1)",
		"//user@evil.com",
		"/%zz",
		" /approvals",
		"\t//evil.com",
	}
	for _, p := range refused {
		if got := auth.LocalReturnPath(p); got != "/" {
			t.Errorf("LocalReturnPath(%q) = %q, want /", p, got)
		}
	}
}
