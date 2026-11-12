package supplychain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// ActionApproveDependencies is the approval action under which the Owner
// approves new dependencies.
const ActionApproveDependencies = "approve_new_dependencies"

// Domain of the payload whose digest is the subject of a dependency approval.
const (
	dependencySubjectDomain  = "sisf.new_dependencies"
	dependencySubjectVersion = 1
)

// Tree reads a repository at a commit. It is how the gate sees the change,
// rather than trusting a list of paths the candidate supplies.
type Tree interface {
	// ChangedPaths lists the repository-relative paths that differ between
	// base and head.
	ChangedPaths(ctx context.Context, base, head core.CommitSHA) ([]string, error)
	// ReadFile returns a file at commit, or NOT_FOUND when it does not exist.
	ReadFile(ctx context.Context, commit core.CommitSHA, path string) ([]byte, error)
}

// Finding is one known vulnerability a scanner reported.
type Finding struct {
	ID     string `json:"id"`
	Module string `json:"module,omitempty"`
}

// ScanResult is the outcome of a vulnerability scan, such as govulncheck,
// run over one commit by a trusted runner.
type ScanResult struct {
	CommitSHA core.CommitSHA `json:"commit_sha"`
	Tool      string         `json:"tool"`
	// Completed is false when the scan did not finish; an unfinished scan
	// is not a clean one.
	Completed   bool      `json:"completed"`
	Findings    []Finding `json:"findings,omitempty"`
	EvidenceRef string    `json:"evidence_ref,omitempty"`
}

// ScanSource supplies the vulnerability scan of a candidate.
type ScanSource interface {
	// Scan returns the scan of the candidate's commit, or NOT_FOUND.
	Scan(ctx context.Context, c qa.Candidate) (ScanResult, error)
}

// DependencyApprovals supplies the Owner's decision on new dependencies.
type DependencyApprovals interface {
	// Approval returns the decision recorded for subject, the digest
	// NewDependencySubject gives, or NOT_FOUND. Only decisions the approval
	// service accepted from a person holding the Owner role are returned.
	Approval(ctx context.Context, c qa.Candidate, subject core.Digest) (approvals.Approval, error)
}

// GateConfig wires the supply-chain gate to its sources.
type GateConfig struct {
	FactoryID core.FactoryID
	Tree      Tree
	Scans     ScanSource
	Approvals DependencyApprovals
	// ApprovalVerifier checks the signature of a dependency approval: a
	// chain verifier trusting the approval service's delegated key.
	ApprovalVerifier core.Verifier
	Clock            core.Clock
}

// Gate is the supply-chain hard gate. It implements qa.HardGate: a change to
// dependencies, lockfiles, base images or CI configuration passes only when
// every pin is exact, the vulnerability scan of the commit is complete and
// clean, and every new dependency carries the Owner's signed approval.
type Gate struct{ cfg GateConfig }

// NewGate returns the gate. Every source is required: a gate that cannot
// look cannot pass anything.
func NewGate(cfg GateConfig) (*Gate, error) {
	if err := cfg.FactoryID.Validate(); err != nil {
		return nil, err
	}
	if cfg.Tree == nil || cfg.Scans == nil || cfg.Approvals == nil || cfg.ApprovalVerifier == nil || cfg.Clock == nil {
		return nil, invalid("the supply-chain gate needs a tree, a scan source, an approval source, an approval verifier and a clock")
	}
	return &Gate{cfg: cfg}, nil
}

// Name is qa.GateSupplyChain.
func (g *Gate) Name() qa.GateName { return qa.GateSupplyChain }

// Check judges the candidate's change between its baseline and its commit.
// An error means the gate could not run, which blocks the candidate as a
// failure does.
func (g *Gate) Check(ctx context.Context, c qa.Candidate) (qa.GateResult, error) {
	if err := c.Validate(); err != nil {
		return qa.GateResult{}, err
	}
	paths, err := g.cfg.Tree.ChangedPaths(ctx, c.BaselineSHA, c.CommitSHA)
	if err != nil {
		return qa.GateResult{}, err
	}
	changes := DetectChanges(paths)
	result := qa.GateResult{Gate: qa.GateSupplyChain}
	if len(changes) == 0 {
		result.Passed = true
		result.Detail = "no dependency, lockfile, base-image or CI configuration change"
		return result, nil
	}
	var problems []string
	added := map[string]Module{}
	for _, change := range changes {
		found, err := g.checkChange(ctx, c, change, added)
		if err != nil {
			return qa.GateResult{}, err
		}
		problems = append(problems, found...)
	}
	scanProblems, evidence, err := g.checkScan(ctx, c)
	if err != nil {
		return qa.GateResult{}, err
	}
	problems = append(problems, scanProblems...)
	if len(added) > 0 {
		approvalProblem, err := g.checkApproval(ctx, c, slices.Collect(maps.Values(added)))
		if err != nil {
			return qa.GateResult{}, err
		}
		if approvalProblem != "" {
			problems = append(problems, approvalProblem)
		}
	}
	result.EvidenceRef = evidence
	if len(problems) == 0 {
		result.Passed = true
		result.Detail = "supply-chain change is pinned, scanned clean and approved"
		return result, nil
	}
	result.Detail = strings.Join(problems, "; ")
	return result, nil
}

// checkChange checks the pins of one changed file and records the modules
// it adds. A file deleted by the change has nothing left to pin.
func (g *Gate) checkChange(ctx context.Context, c qa.Candidate, change Change, added map[string]Module) ([]string, error) {
	if !pinChecked(change) {
		return []string{change.Path + ": pins of this file kind cannot be verified"}, nil
	}
	head, exists, err := g.read(ctx, c.CommitSHA, change.Path)
	if err != nil || !exists {
		return nil, err
	}
	switch change.Kind {
	case KindBaseImage:
		return checkContainerfile(change.Path, head), nil
	case KindCIConfig:
		return checkCIConfig(change.Path, head), nil
	case KindLockfile:
		_, problems := parseGoSum(change.Path, head)
		return problems, nil
	}
	return g.checkGoMod(ctx, c, change.Path, head, added)
}

// checkGoMod requires exact versions, a go.sum hash for every module the
// change adds or moves, and records the modules that are new.
func (g *Gate) checkGoMod(ctx context.Context, c qa.Candidate, file string, head []byte, added map[string]Module) ([]string, error) {
	mod := parseGoMod(file, head)
	problems := mod.problems
	before := map[string]string{}
	if base, exists, err := g.read(ctx, c.BaselineSHA, file); err != nil {
		return nil, err
	} else if exists {
		before = parseGoMod(file, base).requires
	}
	sumFile := path.Join(path.Dir(file), "go.sum")
	sumData, _, err := g.read(ctx, c.CommitSHA, sumFile)
	if err != nil {
		return nil, err
	}
	pinned, _ := parseGoSum(sumFile, sumData)
	for _, modulePath := range slices.Sorted(maps.Keys(mod.requires)) {
		m := Module{Path: modulePath, Version: mod.requires[modulePath]}
		previous, existed := before[modulePath]
		if existed && previous == m.Version {
			continue
		}
		if !pinned[m.String()] {
			problems = append(problems, file+": "+m.String()+" has no hash in "+sumFile)
		}
		if !existed {
			added[m.String()] = m
		}
	}
	return problems, nil
}

// checkScan requires a complete scan of the candidate's commit with no findings.
func (g *Gate) checkScan(ctx context.Context, c qa.Candidate) ([]string, string, error) {
	scan, err := g.cfg.Scans.Scan(ctx, c)
	if core.HasCode(err, core.CodeNotFound) {
		return []string{"no vulnerability scan of the commit was found"}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	switch {
	case scan.CommitSHA != c.CommitSHA:
		return []string{"the vulnerability scan is of another commit"}, scan.EvidenceRef, nil
	case !scan.Completed:
		return []string{"the vulnerability scan did not complete"}, scan.EvidenceRef, nil
	case len(scan.Findings) > 0:
		ids := make([]string, len(scan.Findings))
		for i, f := range scan.Findings {
			ids[i] = f.ID
		}
		return []string{"the vulnerability scan found " + strings.Join(sortedUnique(ids), ", ")}, scan.EvidenceRef, nil
	}
	return nil, scan.EvidenceRef, nil
}

// checkApproval requires the Owner's signed, unexpired approval of exactly
// this set of new dependencies under the candidate's policy.
func (g *Gate) checkApproval(ctx context.Context, c qa.Candidate, deps []Module) (string, error) {
	subject := NewDependencySubject(deps)
	names := make([]string, len(deps))
	for i, d := range deps {
		names[i] = d.String()
	}
	need := "new dependencies " + strings.Join(sortedUnique(names), ", ") + " need the Owner's approval"
	a, err := g.cfg.Approvals.Approval(ctx, c, subject)
	if core.HasCode(err, core.CodeNotFound) {
		return need, nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case a.Action != ActionApproveDependencies || a.FactoryID != g.cfg.FactoryID || a.ActorKind != core.ActorHuman:
		return need + ": the approval found is for another action, factory or actor", nil
	case g.cfg.ApprovalVerifier.Verify(ctx, a.SigningPayload(), a.Signature) != nil:
		return need + ": the approval found is not validly signed", nil
	case a.Authorizes(subject, c.Policy.PolicySHA, g.cfg.Clock.Now()) != nil:
		return need + ": the approval found does not authorize this set under this policy now", nil
	}
	return "", nil
}

// read returns a file at commit and whether it exists.
func (g *Gate) read(ctx context.Context, commit core.CommitSHA, file string) ([]byte, bool, error) {
	data, err := g.cfg.Tree.ReadFile(ctx, commit, file)
	if core.HasCode(err, core.CodeNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// NewDependencySubject is the digest the Owner approves for a set of new
// modules. It covers the sorted set, so an approval of one set never
// authorizes another.
func NewDependencySubject(deps []Module) core.Digest {
	names := make([]string, len(deps))
	for i, d := range deps {
		names[i] = d.String()
	}
	names = sortedUnique(names)
	b := core.NewPayloadBuilder(dependencySubjectDomain, dependencySubjectVersion).Int("modules", int64(len(names)))
	for _, name := range names {
		b.Text("module", name)
	}
	sum := sha256.Sum256(b.Bytes())
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}
