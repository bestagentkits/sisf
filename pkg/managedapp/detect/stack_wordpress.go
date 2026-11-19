package detect

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// wordpress knows a WordPress site, with or without WooCommerce, run from
// the official image. Its content is bundled from a lockfile of pinned core
// and plugin archives plus the repository's own plugins, so no candidate code
// runs while a revision is made.
type wordpress struct{}

func (wordpress) Name() string { return "wordpress" }

func (wordpress) Match(app *App) bool {
	return app.compose != nil && imageName(app.webService().Image) == "wordpress"
}

// wordpressLock is the lockfile a WordPress repository keeps.
const wordpressLock = "content.lock"

// sensitivePlugin names the repository plugins that move money or set
// prices: the Owner approves every change to them.
var sensitivePlugin = regexp.MustCompile(`(?i)(pay|checkout|gateway|tax|price|coupon|refund)`)

func (wordpress) Describe(app *App, s *managedapp.Spec) error {
	if !app.HasFile(wordpressLock) {
		return fmt.Errorf("a WordPress site is adopted from a lockfile: add %s pinning core and every plugin by version and sha256 (see examples/woocommerce-app)", wordpressLock)
	}
	if s.Runtime.User == "" {
		s.Runtime.User = "33:33"
		app.Note("runtime.user: the Compose file names none; www-data (33:33) is used, never root")
	}
	if s.Runtime.Env == nil {
		s.Runtime.Env = map[string]string{}
	}
	// Apache keeps its run files under /tmp: the root file system is read-only.
	for k, v := range map[string]string{
		"APACHE_RUN_DIR": "/tmp/apache2", "APACHE_PID_FILE": "/tmp/apache2/apache2.pid",
		"APACHE_LOCK_DIR": "/tmp/apache2/lock", "APACHE_LOG_DIR": "/tmp/apache2/log",
	} {
		if _, set := s.Runtime.Env[k]; !set {
			s.Runtime.Env[k] = v
		}
	}
	s.Runtime.HealthPath = "/healthz"
	if !app.HasFile("mu-plugins/sisf-health.php") {
		app.Note("add mu-plugins/sisf-health.php (see examples/woocommerce-app): without it no revision reports itself healthy")
	}
	s.Bundle = managedapp.Bundle{Lock: wordpressLock, Mount: "/var/www/html", Prune: []string{"wp-content/plugins/akismet", "wp-content/plugins/hello.php"}}
	for _, src := range []managedapp.Source{
		{From: "plugins", To: "wp-content/plugins"}, {From: "themes", To: "wp-content/themes"},
		{From: "mu-plugins", To: "wp-content/mu-plugins"}, {From: "wp-config.php", To: "wp-config.php"},
		{From: "htaccess", To: ".htaccess"},
	} {
		if _, err := os.Lstat(filepath.Join(app.Dir, src.From)); err == nil {
			s.Bundle.Sources = append(s.Bundle.Sources, src)
		}
	}
	for i, v := range s.Data {
		if strings.HasPrefix(v.Target, s.Bundle.Mount+"/") && v.Owner == "" {
			s.Data[i].Owner = s.Runtime.User
		}
	}
	s.Policy = managedapp.Policy{
		Protected:   wordpressProtected(app),
		LockChanges: map[managedapp.LockChange]managedapp.Decision{managedapp.LockPatch: managedapp.DecisionAuto},
	}
	s.Checks = wordpressChecks(app)
	return nil
}

// wordpressProtected returns the paths only the Owner may change: what runs
// on every request (must-use plugins, configuration, rewrite rules) and the
// repository plugins that touch payment, checkout, prices or tax.
func wordpressProtected(app *App) []string {
	out := []string{"mu-plugins/*", "wp-config.php", "htaccess"}
	entries, _ := os.ReadDir(filepath.Join(app.Dir, "plugins"))
	for _, e := range entries {
		if e.IsDir() && sensitivePlugin.MatchString(e.Name()) {
			out = append(out, "plugins/"+e.Name()+"/*")
			app.Note("plugins/%s is protected: its name says it handles payment, checkout, prices or tax", e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// wordpressChecks returns the behaviour every revision must keep: the site
// and its health answer, and, for a shop, the catalogue and a purchase.
func wordpressChecks(app *App) []managedapp.Check {
	checks := []managedapp.Check{
		{Name: "health", MaxLatencyMS: 2000, Steps: []managedapp.Step{{Path: "/healthz", JSON: map[string]string{"database": "ok"}}}},
		{Name: "home", MaxLatencyMS: 3000, Steps: []managedapp.Step{{Path: "/", Contains: "</html>"}}},
	}
	lock, _ := os.ReadFile(filepath.Join(app.Dir, wordpressLock))
	if !regexp.MustCompile(`(?m)^plugin\s+woocommerce\s`).Match(lock) {
		return checks
	}
	payment := "cod"
	if _, err := os.Stat(filepath.Join(app.Dir, "plugins", "sisf-sandbox-pay")); err == nil {
		payment = "sisf_sandbox"
	}
	app.Note("checks: the purchase check pays with %q; it runs only against a disposable copy of the data", payment)
	address := `"first_name":"Synthetic","last_name":"Buyer","address_1":"1 Example Street","city":"Sampletown","state":"CA","postcode":"94000","country":"US"`
	return append(checks,
		managedapp.Check{Name: "catalogue", MaxLatencyMS: 3000, Steps: []managedapp.Step{
			{Path: "/wp-json/wc/store/v1/products?per_page=5", Contains: `"prices"`},
		}},
		managedapp.Check{Name: "purchase", MaxLatencyMS: 8000, Writes: true, Steps: []managedapp.Step{
			{Path: "/wp-json/wc/store/v1/cart", CaptureHeader: map[string]string{"nonce": "Nonce"}},
			{Path: "/wp-json/wc/store/v1/products?per_page=1&stock_status=instock", CaptureJSON: map[string]string{"product": "0.id"}},
			{Method: "POST", Path: "/wp-json/wc/store/v1/cart/add-item", Headers: map[string]string{"Nonce": "${nonce}"},
				Body: `{"id":${product},"quantity":1}`, Status: 201},
			{Method: "POST", Path: "/wp-json/wc/store/v1/checkout", Headers: map[string]string{"Nonce": "${nonce}"},
				Body: `{"billing_address":{` + address + `,"email":"synthetic@shop.example.invalid"},"shipping_address":{` + address + `},"payment_method":"` + payment + `"}`,
				JSON: map[string]string{"payment_result.payment_status": "success"}},
		}},
	)
}
