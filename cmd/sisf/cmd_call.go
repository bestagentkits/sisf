package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

const maxResponseBytes = 8 << 20

var actionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// parseInterspersed parses flags that may follow the positional arguments
// and returns the positional ones, exactly want of them.
func parseInterspersed(fs *flag.FlagSet, args []string, want int) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usagef("%v", err)
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(positional) != want {
		return nil, usagef("expected %d argument(s), got %d", want, len(positional))
	}
	return positional, nil
}

// apiClient talks to the REST surface of one factory.
type apiClient struct {
	base, token string
	hc          *http.Client
}

// do sends one request and returns the status, the body and the headers. It
// never follows a redirect, so the token cannot be handed to another host.
func (c *apiClient) do(ctx context.Context, method, path string, body io.Reader) (int, []byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return 0, nil, nil, usagef("the control URL is not usable")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, nil, core.NewError(core.CodeUnavailable, "the factory at "+c.base+" cannot be reached").WithCause(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, nil, core.NewError(core.CodeUnavailable, "the answer of the factory could not be read").WithCause(err)
	}
	return resp.StatusCode, raw, resp.Header, nil
}

// postJSON posts v and decodes a 200 answer into out; any other answer is a
// typed failure.
func (c *apiClient) postJSON(ctx context.Context, path string, v, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return core.NewError(core.CodeInternal, "the request cannot be encoded").WithCause(err)
	}
	status, raw, _, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return failureOf(status, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return core.NewError(core.CodeUnavailable, "the factory answered with something that is not JSON")
	}
	return nil
}

// failureOf turns an error answer into the typed error it carries. An answer
// that is not one, such as a proxy's page, is UNAVAILABLE.
func failureOf(status int, raw []byte) error {
	var body struct {
		Error core.Error `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil || !body.Error.Code.Valid() {
		return core.NewError(core.CodeUnavailable, fmt.Sprintf("the factory answered HTTP %d without a typed error", status))
	}
	out := core.NewError(body.Error.Code, body.Error.Message, body.Error.NextActions...)
	out.Details = body.Error.Details
	return out
}

// callOptions are the per-call fields of the REST body.
type callOptions struct {
	dryRun   bool
	approval string
	key      string
}

func (o *callOptions) register(fs *flag.FlagSet, withApproval bool) {
	fs.BoolVar(&o.dryRun, "dry-run", false, "report what would change and change nothing")
	fs.StringVar(&o.key, "idempotency-key", "", "a retry with the same key and input gets the first answer without writing again")
	if withApproval {
		fs.StringVar(&o.approval, "approval", "", "reference of the approved request this call spends")
	}
}

// callAction runs one action on the factory and prints the JSON result.
func callAction(ctx context.Context, env *Env, conn *connFlags, action string, input any, opts callOptions) error {
	if !actionNamePattern.MatchString(action) {
		return usagef("%q is not an action name such as runs.explain", action)
	}
	client, _, err := conn.client(true)
	if err != nil {
		return err
	}
	body := map[string]any{"input": input}
	if opts.dryRun {
		body["dry_run"] = true
	}
	if opts.approval != "" {
		body["approval_id"] = opts.approval
	}
	if opts.key != "" {
		body["idempotency_key"] = opts.key
	}
	var out json.RawMessage
	if err := client.postJSON(ctx, actionPath+action, body, &out); err != nil {
		return err
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, out, "", "  ") != nil {
		pretty.Reset()
		pretty.Write(out)
	}
	return env.Printf("%s\n", pretty.String())
}

func init() {
	register(Command{
		Path:    []string{"call"},
		Args:    "<action> [--input json|-] [--dry-run] [--approval id] [--idempotency-key key] [--url url]",
		Summary: "Call any action of the running factory over its REST surface",
		Help: `
Calls one action, for example "sisf call runs.explain --input '{"issue":"acme/app#12"}'",
and prints the JSON result. --input is a JSON object, or - to read it from
standard input. A failure prints the typed error code, the message and the
next valid steps, and exits non-zero.

The control URL and the token come from --url / $` + controlURLEnv + ` and $` + tokenEnv + `, else from the
login stored by "sisf login". The token is sent only to the URL it was issued for.`,
		Run: func(ctx context.Context, env *Env, args []string) error {
			fs := flag.NewFlagSet("call", flag.ContinueOnError)
			fs.SetOutput(env.Stderr)
			var conn connFlags
			var opts callOptions
			conn.register(fs)
			opts.register(fs, true)
			input := fs.String("input", "", "JSON input of the action, or - for standard input")
			pos, err := parseInterspersed(fs, args, 1)
			if err != nil {
				return err
			}
			raw := strings.TrimSpace(*input)
			if raw == "-" {
				data, rerr := io.ReadAll(io.LimitReader(env.Stdin, maxResponseBytes))
				if rerr != nil {
					return core.NewError(core.CodeUnavailable, "the input could not be read from standard input").WithCause(rerr)
				}
				raw = strings.TrimSpace(string(data))
			}
			if raw == "" {
				raw = "{}"
			}
			if !json.Valid([]byte(raw)) {
				return usagef("--input is not valid JSON")
			}
			return callAction(ctx, env, &conn, pos[0], json.RawMessage(raw), opts)
		},
	})
}

// alias is a friendly command that builds the input of one action and calls
// it. It holds no logic of its own: the action service decides everything.
type alias struct {
	path    []string
	args    string
	summary string
	action  string
	// positional is how many arguments follow the path.
	positional int
	// flags registers the alias's own flags and returns the input builder.
	flags func(fs *flag.FlagSet, opts *callOptions) func(pos []string) (any, error)
}

func (a alias) register() {
	register(Command{Path: a.path, Args: a.args, Summary: a.summary, Help: "Same as: sisf call " + a.action,
		Run: func(ctx context.Context, env *Env, args []string) error {
			fs := flag.NewFlagSet(strings.Join(a.path, " "), flag.ContinueOnError)
			fs.SetOutput(env.Stderr)
			var conn connFlags
			var opts callOptions
			conn.register(fs)
			build := a.flags(fs, &opts)
			pos, err := parseInterspersed(fs, args, a.positional)
			if err != nil {
				return err
			}
			input, err := build(pos)
			if err != nil {
				return err
			}
			return callAction(ctx, env, &conn, a.action, input, opts)
		}})
}

// noFlags is for an alias that takes only its positional arguments.
func noFlags(build func(pos []string) (any, error)) func(*flag.FlagSet, *callOptions) func([]string) (any, error) {
	return func(*flag.FlagSet, *callOptions) func([]string) (any, error) { return build }
}

// target names an issue ("owner/repo#12") or a run ID.
func target(id string) any {
	if strings.ContainsAny(id, "/#") {
		return map[string]string{"issue": id}
	}
	return map[string]string{"run_id": id}
}

// listFlags is the shared shape of the list aliases: a repeatable filter and
// a limit.
func listFlags(filter, usage string) func(*flag.FlagSet, *callOptions) func([]string) (any, error) {
	return func(fs *flag.FlagSet, _ *callOptions) func([]string) (any, error) {
		var values stringList
		fs.Var(&values, filter, usage)
		limit := fs.Int("limit", 0, "most entries to return")
		return func([]string) (any, error) {
			in := map[string]any{}
			if len(values) > 0 {
				in[map[string]string{"state": "states", "status": "statuses"}[filter]] = []string(values)
			}
			if *limit > 0 {
				in["limit"] = *limit
			}
			return in, nil
		}
	}
}

func decideFlags(fs *flag.FlagSet, opts *callOptions) func([]string) (any, error) {
	opts.register(fs, false)
	decision := fs.String("decision", "", "approve, reject or request_changes")
	nonce := fs.String("nonce", "", "nonce shown with the request")
	digest := fs.String("subject-digest", "", "subject digest shown with the request")
	policy := fs.String("policy-revision", "", "policy revision shown with the request")
	reason := fs.String("reason", "", "why")
	budget := fs.Int64("budget-cap", -1, "condition: spending cap in micros")
	expires := fs.String("expires-at", "", "condition: RFC 3339 time the approval lapses")
	return func(pos []string) (any, error) {
		if *decision == "" || *nonce == "" || *digest == "" || *policy == "" {
			return nil, usagef("--decision, --nonce, --subject-digest and --policy-revision are required; \"sisf approvals get %s\" shows the last three", pos[0])
		}
		in := map[string]any{"request_id": pos[0], "decision": *decision, "nonce": *nonce, "subject_digest": *digest, "policy_revision": *policy}
		if *reason != "" {
			in["reason"] = *reason
		}
		conditions := map[string]any{}
		if *budget >= 0 {
			conditions["budget_cap"] = *budget
		}
		if *expires != "" {
			conditions["expires_at"] = *expires
		}
		if len(conditions) > 0 {
			in["conditions"] = conditions
		}
		return in, nil
	}
}

func init() {
	idOf := func(pos []string) (any, error) { return target(pos[0]), nil }
	for _, a := range []alias{
		{path: []string{"status"}, summary: "Show the factory overview", action: "factory.status",
			flags: noFlags(func([]string) (any, error) { return map[string]any{}, nil })},
		{path: []string{"issues"}, args: "[--state s]... [--limit n]", summary: "List tracked issues", action: "issues.list",
			flags: listFlags("state", "lifecycle state to keep (repeatable)")},
		{path: []string{"runs", "inspect"}, args: "<issue|run-id>", summary: "Show the record of an issue and its current run", action: "runs.inspect",
			positional: 1, flags: noFlags(idOf)},
		{path: []string{"runs", "explain"}, args: "<issue|run-id>", summary: "Explain why a run is stopped or blocked", action: "runs.explain",
			positional: 1, flags: noFlags(idOf)},
		{path: []string{"approvals", "list"}, args: "[--status s]... [--limit n]", summary: "List approval requests", action: "approvals.list",
			flags: listFlags("status", "request status to keep (repeatable)")},
		{path: []string{"approvals", "get"}, args: "<id>", summary: "Show an approval request with its evidence and decision", action: "approvals.get",
			positional: 1, flags: noFlags(func(pos []string) (any, error) { return map[string]string{"id": pos[0]}, nil })},
		{path: []string{"approvals", "decide"}, args: "<id> --decision d --nonce n --subject-digest d --policy-revision r [--reason text] [--dry-run]",
			summary: "Approve, reject or request changes on an approval request", action: "approvals.decide", positional: 1, flags: decideFlags},
	} {
		a.register()
	}
}
