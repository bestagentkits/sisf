package ui

import (
	"fmt"
	"html/template"
	"strings"
)

// The pages of the dashboard. They share dash-layout.html.
const (
	PageDashSignIn    = "dash-signin"
	PageDashError     = "dash-error"
	PageDashResult    = "dash-result"
	PageDashOverview  = "dash-overview"
	PageDashWorkboard = "dash-workboard"
	PageDashRuns      = "dash-runs"
	PageDashTelemetry = "dash-telemetry"
	PageDashApprovals = "dash-approvals"
	PageDashApproval  = "dash-approval"
	PageDashProduct   = "dash-product"
	PageDashSecurity  = "dash-security"
	PageDashSetup     = "dash-setup"
	PageDashBlocked   = "dash-blocked"
)

var dashboardPages = []string{
	PageDashSignIn, PageDashError, PageDashResult, PageDashOverview, PageDashWorkboard, PageDashRuns,
	PageDashTelemetry, PageDashApprovals, PageDashApproval, PageDashProduct, PageDashSecurity, PageDashSetup,
	PageDashBlocked,
}

// LoadDashboard parses every page of the dashboard together with its
// layout. It fails at start up on a template error, so a broken page is
// never found by a visitor.
func LoadDashboard() (*Pages, error) {
	funcs := template.FuncMap{"utc": formatUTC, "iso": formatISO}
	pages := &Pages{pages: map[string]*template.Template{}}
	for _, name := range dashboardPages {
		t, err := template.New(name).Funcs(funcs).ParseFS(templateFiles, "templates/dash-layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse dashboard page %s: %w", name, err)
		}
		pages.pages[name] = t
	}
	return pages, nil
}

// ContentSecurityPolicy is the policy the dashboard sends. Scripts and
// styles come only from the server itself, so a page holds no inline script,
// no inline style and no inline event handler. Forms may post to the server;
// formOrigins adds the origins a form POST may be redirected to, which a
// browser also checks, for example the sign-in provider that a step-up
// confirmation leads to.
func ContentSecurityPolicy(formOrigins ...string) string {
	formAction := append([]string{"'self'"}, formOrigins...)
	return strings.Join([]string{
		"default-src 'none'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"form-action " + strings.Join(formAction, " "),
		"base-uri 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}
