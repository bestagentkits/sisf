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
	"strconv"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
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
	if req.Key != "" {
		args = append(args, "--idempotency-key", req.Key)
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
	pageCode         = regexp.MustCompile(`data-error-code="([A-Z_]+)"><strong>[A-Z_]+</strong> ([^<]*)`)
	pageOutput       = regexp.MustCompile(`(?s)<pre class="output">(.*?)</pre>`)
	pageDone         = regexp.MustCompile(`data-state="done"`)
	pageStepUpAction = regexp.MustCompile(`<input type="hidden" name="action" value="([^"]+)">`)
	pageStepUpTarget = regexp.MustCompile(`<input type="hidden" name="target" value="([^"]+)">`)
)

// dashboard posts the action's form with the person's session.
func (c *clients) dashboard(t *testing.T, action string, req call) outcome {
	t.Helper()
	if req.DryRun || req.Key != "" || !slices.Contains(dashboardForms, action) {
		t.Fatalf("the dashboard has no form for %s (dry run %v, idempotency key %q)", action, req.DryRun, req.Key)
	}
	form := url.Values{auth.CSRFFormField: {c.person.CSRF()}, "return_to": {"/"}}
	var fields map[string]any
	if len(req.Input) > 0 {
		if err := json.Unmarshal(req.Input, &fields); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range fields {
		switch v := v.(type) {
		case string:
			form.Set(k, v)
		case float64:
			form.Set(k, strconv.FormatFloat(v, 'f', -1, 64))
		case bool:
			if v {
				form.Set(k, "1")
			}
		}
	}
	if req.ApprovalID != "" {
		form.Set("approval_id", req.ApprovalID)
	}
	resp := c.person.Do(http.MethodPost, "/actions/"+action, strings.NewReader(form.Encode()),
		harness.NavigatedForm())
	raw, _ := io.ReadAll(resp.Body)
	if m := pageCode.FindSubmatch(raw); m != nil {
		out := outcome{Code: core.ErrorCode(m[1]), Message: html.UnescapeString(string(m[2]))}
		a, g := pageStepUpAction.FindSubmatch(raw), pageStepUpTarget.FindSubmatch(raw)
		if a != nil && g != nil {
			out.Details = map[string]string{"step_up_action": html.UnescapeString(string(a[1])),
				"step_up_target": html.UnescapeString(string(g[1]))}
		}
		return out
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

// confirmed posts the action's dashboard form as the person, and when the
// answer asks for a step-up, confirms exactly the call it names, as the
// result page offers, and posts the form again.
func (c *clients) confirmed(t *testing.T, action string, req call) outcome {
	t.Helper()
	got := c.dashboard(t, action, req)
	if got.Code != core.CodeStepUpRequired {
		return got
	}
	if got.Details["step_up_action"] != action || got.Details["step_up_target"] == "" {
		t.Fatalf("the dashboard asks for a step-up of %+v for %s", got.Details, action)
	}
	c.person.StepUpFor(got.Details["step_up_action"], got.Details["step_up_target"])
	return c.dashboard(t, action, req)
}
