package acceptance

import (
	"bytes"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

// cli runs "sisf call" with the person's token.
func (c *clients) cli(t *testing.T, action string, req call) outcome {
	t.Helper()
	args := []string{"call", action, "--url", c.f.Base(), "--input", string(req.Input)}
	if req.DryRun {
		args = append(args, "--dry-run")
	}
	if req.ApprovalID != "" {
		args = append(args, "--approval", req.ApprovalID)
	}
	cmd := exec.Command(sisfBinary, args...)
	cmd.Env = []string{"HOME=" + t.TempDir(), "SISF_TOKEN=" + c.pat, "PATH=" + os.Getenv("PATH")}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		m := cliError.FindStringSubmatch(stderr.String())
		if m == nil {
			t.Fatalf("sisf call %s failed without a typed error: %v\n%s", action, err, stderr.String())
		}
		return outcome{Code: core.ErrorCode(m[1]), Message: strings.TrimSpace(m[2])}
	}
	return parseResult(t, stdout.Bytes())
}

var cliError = regexp.MustCompile(`(?m)^sisf: ([A-Z_]+): (.*)$`)

// The result page of a dashboard form.
var (
	pageCode   = regexp.MustCompile(`data-error-code="([A-Z_]+)"><strong>[A-Z_]+</strong> ([^<]*)`)
	pageOutput = regexp.MustCompile(`(?s)<pre class="output">(.*?)</pre>`)
	pageDone   = regexp.MustCompile(`data-state="done"`)
)

// dashboard posts the action's form with the person's session.
func (c *clients) dashboard(t *testing.T, action string, req call) outcome {
	t.Helper()
	if req.DryRun || !slices.Contains(dashboardForms, action) {
		t.Fatalf("the dashboard has no form for %s (dry run %v)", action, req.DryRun)
	}
	form := url.Values{auth.CSRFFormField: {c.person.CSRF()}, "return_to": {"/"}}
	var fields map[string]any
	if len(req.Input) > 0 {
		if err := json.Unmarshal(req.Input, &fields); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range fields {
		if s, ok := v.(string); ok {
			form.Set(k, s)
		}
	}
	if req.ApprovalID != "" {
		form.Set("approval_id", req.ApprovalID)
	}
	resp := c.person.Do(http.MethodPost, "/actions/"+action, strings.NewReader(form.Encode()),
		http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	raw, _ := io.ReadAll(resp.Body)
	if m := pageCode.FindSubmatch(raw); m != nil {
		return outcome{Code: core.ErrorCode(m[1]), Message: html.UnescapeString(string(m[2]))}
	}
	if !pageDone.Match(raw) {
		t.Fatalf("the dashboard answered %d with neither a result nor an error", resp.StatusCode)
	}
	out := outcome{}
	if m := pageOutput.FindSubmatch(raw); m != nil {
		out.Output = canonical(json.RawMessage(html.UnescapeString(string(m[1]))))
	}
	return out
}
