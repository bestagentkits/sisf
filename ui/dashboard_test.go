package ui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestDashboardPagesLoad(t *testing.T) {
	t.Parallel()
	if _, err := LoadDashboard(); err != nil {
		t.Fatal(err)
	}
}

func TestTemplatesCarryNoInlineScriptHandlerOrStyle(t *testing.T) {
	t.Parallel()
	handler := regexp.MustCompile(`(?i)<[a-z][^>]*\s(on[a-z]+|style)\s*=`)
	inlineScript := regexp.MustCompile(`(?i)<script(\s[^>]*)?>`)
	err := fs.WalkDir(templateFiles, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(templateFiles, path)
		if err != nil {
			return err
		}
		text := string(raw)
		if handler.MatchString(text) {
			t.Errorf("%s holds an inline event handler or style attribute", path)
		}
		for _, tag := range inlineScript.FindAllString(text, -1) {
			if !strings.Contains(tag, "src=") {
				t.Errorf("%s holds an inline script", path)
			}
		}
		if strings.Contains(text, "javascript:") || regexp.MustCompile(`(?i)(src|href)="https?://`).MatchString(text) {
			t.Errorf("%s loads or links something from outside the server", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestContentSecurityPolicyAllowsNoUnsafeSource(t *testing.T) {
	t.Parallel()
	csp := ContentSecurityPolicy("https://idp.example.test")
	if strings.Contains(csp, "unsafe") || strings.Contains(csp, "*") {
		t.Errorf("policy is loose: %s", csp)
	}
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "form-action 'self' https://idp.example.test"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy %q lacks %q", csp, want)
		}
	}
}
