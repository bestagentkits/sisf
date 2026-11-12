package catalog

import (
	"context"
	"encoding/json"
	"io/fs"

	"github.com/bestagentkits/sisf/internal/actions"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Names of the catalog actions. They replace the stand-ins of the same
// names with the same sensitivity.
const (
	ActionFeaturesList           = "features.list"
	ActionChangelogList          = "changelog.list"
	ActionDocsList               = "docs.list"
	ActionDecisionsProposeUpdate = "decisions.propose_update"
)

// DefaultDocsRoot is the docs directory listed when none is configured.
const DefaultDocsRoot = "docs"

// Bounds of changelog.list.
const (
	defaultChangelogLimit = 50
	maxChangelogLimit     = 500
)

// ActionsConfig is what the catalog actions are built from.
type ActionsConfig struct {
	// Publisher reads the repository at its base branch's head and opens
	// the pull requests of decisions.propose_update.
	Publisher *Publisher
	// Docs holds the docs tree listed under DocsRoot; required.
	Docs     fs.FS
	DocsRoot string
}

var (
	readers   = []core.Role{core.RoleOwner, core.RoleMaintainer, core.RoleReporter, core.RoleViewer, core.RoleAgent}
	proposers = []core.Role{core.RoleOwner, core.RoleMaintainer, core.RoleAgent}
)

const (
	emptyInput     = `{"type":"object","additionalProperties":false}`
	changelogInput = `{"type":"object","additionalProperties":false,"properties":{"limit":{"type":"integer","minimum":0,"maximum":500,"description":"Most entries to return, newest first; 0 means 50."}}}`
	proposeInput   = `{"type":"object","additionalProperties":false,"required":["content","summary"],"properties":{` +
		`"content":{"type":"string","description":"The complete proposed DECISIONS.md."},` +
		`"summary":{"type":"string","maxLength":200},"rationale":{"type":"string","maxLength":2000}}}`
	featuresOutput  = `{"type":"object","properties":{"revision":{"type":"string"},"features":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"scope":{"type":"string"},"status":{"type":"string"}}}}}}`
	changelogOutput = `{"type":"object","properties":{"revision":{"type":"string"},"entries":{"type":"array","items":{"type":"object","properties":{"date":{"type":"string"},"text":{"type":"string"},"run_id":{"type":"string"}}}}}}`
	docsOutput      = `{"type":"object","properties":{"root":{"type":"string"},"docs":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"title":{"type":"string"}}}}}}`
	proposeOutput   = `{"type":"object","properties":{"branch":{"type":"string"},"base":{"type":"string"},"pull_request":{"type":"string"},` +
		`"requires_owner_approval":{"type":"boolean"},"applied":{"type":"boolean"}}}`
)

// Actions returns features.list, changelog.list and docs.list, which read,
// and decisions.propose_update, which only ever opens a pull request the
// Owner must approve and merge.
func Actions(cfg ActionsConfig) ([]core.Action, error) {
	if cfg.Publisher == nil || cfg.Docs == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the catalog actions need a publisher and a docs tree")
	}
	if cfg.DocsRoot == "" {
		cfg.DocsRoot = DefaultDocsRoot
	}
	h := catalogHandlers{cfg}
	spec := func(name, summary string, s core.Sensitivity, scope string, roles []core.Role, in, out string) core.ActionSpec {
		return core.ActionSpec{Name: name, Summary: summary, Sensitivity: s, Scopes: []string{scope}, Roles: roles,
			Input: actions.Schema(in), Output: actions.Schema(out)}
	}
	return []core.Action{
		{Spec: spec(ActionFeaturesList, "List the feature catalog.", core.SensitivityRead, "catalog:read", readers, emptyInput, featuresOutput), Handle: h.features},
		{Spec: spec(ActionChangelogList, "List the changelog entries of released work.", core.SensitivityRead, "catalog:read", readers, changelogInput, changelogOutput), Handle: h.changelog},
		{Spec: spec(ActionDocsList, "List the docs, guides and ADRs of the product.", core.SensitivityRead, "catalog:read", readers, emptyInput, docsOutput), Handle: h.docs},
		{Spec: spec(ActionDecisionsProposeUpdate, "Propose a change to DECISIONS.md as a pull request the Owner must approve; never applied directly.",
			core.SensitivityWrite, "decisions:write", proposers, proposeInput, proposeOutput), Handle: h.proposeUpdate},
	}, nil
}

type catalogHandlers struct{ cfg ActionsConfig }

// head is the commit the read actions show: the base branch's head, which
// holds only what the Owner merged.
func (h catalogHandlers) head(ctx context.Context) (core.CommitSHA, error) {
	pub := h.cfg.Publisher.cfg
	return pub.Branches.Head(ctx, pub.Repo, pub.BaseBranch)
}

func (h catalogHandlers) features(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	if _, err := actions.DecodeInput[struct{}](req); err != nil {
		return core.ActionResult{}, err
	}
	rev, err := h.head(ctx)
	if err != nil {
		return core.ActionResult{}, err
	}
	pub := h.cfg.Publisher.cfg
	features, err := ReadFeatures(ctx, pub.Files, pub.Repo, rev)
	if err != nil {
		return core.ActionResult{}, err
	}
	return actions.JSONResult(map[string]any{"revision": rev, "features": features})
}

func (h catalogHandlers) changelog(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	in, err := actions.DecodeInput[struct {
		Limit int `json:"limit"`
	}](req)
	if err != nil {
		return core.ActionResult{}, err
	}
	if in.Limit < 0 || in.Limit > maxChangelogLimit {
		return core.ActionResult{}, core.NewError(core.CodeInvalidArgument, "limit must be within 0-500")
	}
	if in.Limit == 0 {
		in.Limit = defaultChangelogLimit
	}
	rev, err := h.head(ctx)
	if err != nil {
		return core.ActionResult{}, err
	}
	content, err := h.cfg.Publisher.read(ctx, rev, PathChangelog)
	if err != nil {
		return core.ActionResult{}, err
	}
	return actions.JSONResult(map[string]any{"revision": rev, "entries": ListChangelog(content, in.Limit)})
}

func (h catalogHandlers) docs(_ context.Context, req core.ActionRequest) (core.ActionResult, error) {
	if _, err := actions.DecodeInput[struct{}](req); err != nil {
		return core.ActionResult{}, err
	}
	docs, err := ListDocs(h.cfg.Docs, h.cfg.DocsRoot)
	if err != nil {
		return core.ActionResult{}, err
	}
	return actions.JSONResult(map[string]any{"root": h.cfg.DocsRoot, "docs": docs})
}

// proposeUpdate opens the pull request. The caller's approval id plays no
// part: approval is the Owner's merge, which this action cannot perform.
func (h catalogHandlers) proposeUpdate(ctx context.Context, req core.ActionRequest) (core.ActionResult, error) {
	u, err := actions.DecodeInput[DecisionsUpdate](req)
	if err != nil {
		return core.ActionResult{}, err
	}
	out, err := h.cfg.Publisher.ProposeDecisionsUpdate(ctx, req.Actor, u, req.DryRun)
	if err != nil {
		return core.ActionResult{}, err
	}
	if !req.DryRun {
		return actions.JSONResult(out)
	}
	after, err := json.Marshal(out)
	if err != nil {
		return core.ActionResult{}, core.NewError(core.CodeInternal, "the plan cannot be encoded").WithCause(err)
	}
	pub := h.cfg.Publisher.cfg
	return core.ActionResult{Plan: &core.DryRun{
		Changes:          []core.Change{{Resource: "pull_request:" + pub.Repo.String() + ":" + out.Branch, Op: core.ChangeCreate, After: after}},
		RequiresApproval: true,
		Warnings:         []string{"The change takes effect only when the Owner approves and merges the pull request."},
	}}, nil
}
