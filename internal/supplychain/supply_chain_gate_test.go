package supplychain

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// fakeTree is a repository with two commits held in memory.
type fakeTree struct {
	files   map[core.CommitSHA]map[string]string
	changed []string
	err     error
}

func (f *fakeTree) ChangedPaths(_ context.Context, base, head core.CommitSHA) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	if base != baseSHA || head != headSHA {
		return nil, core.NewError(core.CodeNotFound, "unknown commits")
	}
	return f.changed, nil
}

func (f *fakeTree) ReadFile(_ context.Context, commit core.CommitSHA, path string) ([]byte, error) {
	data, ok := f.files[commit][path]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "no such file")
	}
	return []byte(data), nil
}

// fakeScans returns one scan result.
type fakeScans struct {
	result ScanResult
	err    error
}

func (f *fakeScans) Scan(context.Context, qa.Candidate) (ScanResult, error) { return f.result, f.err }

// fakeApprovals holds approvals by subject.
type fakeApprovals map[core.Digest]approvals.Approval

func (f fakeApprovals) Approval(_ context.Context, _ qa.Candidate, subject core.Digest) (approvals.Approval, error) {
	a, ok := f[subject]
	if !ok {
		return approvals.Approval{}, core.NewError(core.CodeNotFound, "no approval")
	}
	return a, nil
}

const baseGoMod = `module github.com/acme/app

go 1.25

require (
	gopkg.in/yaml.v3 v3.0.1
)
`

// fixture is a gate over a fake repository, with a clean scan.
type fixture struct {
	tr        *trust
	tree      *fakeTree
	scans     *fakeScans
	approvals fakeApprovals
	gate      *Gate
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	tr := newTrust(t)
	f := &fixture{
		tr:        tr,
		tree:      &fakeTree{files: map[core.CommitSHA]map[string]string{baseSHA: {"go.mod": baseGoMod}, headSHA: {"go.mod": baseGoMod}}},
		scans:     &fakeScans{result: ScanResult{CommitSHA: headSHA, Tool: "govulncheck", Completed: true, EvidenceRef: "scans/head.json"}},
		approvals: fakeApprovals{},
	}
	gate, err := NewGate(GateConfig{FactoryID: testFactory, Tree: f.tree, Scans: f.scans, Approvals: f.approvals, ApprovalVerifier: tr.verifier, Clock: tr.clock})
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	f.gate = gate
	return f
}

// change sets a file at head and marks it changed.
func (f *fixture) change(path, content string) {
	f.tree.files[headSHA][path] = content
	f.tree.changed = append(f.tree.changed, path)
}

// addModule changes go.mod to require a module, and go.sum to pin it when sum is set.
func (f *fixture) addModule(module, version string, sum bool) {
	f.change("go.mod", strings.Replace(baseGoMod, ")", "\t"+module+" "+version+"\n)", 1))
	if sum {
		h1, _ := moduleSum(module)
		f.change("go.sum", module+" "+version+" "+h1+"\n"+module+" "+version+"/go.mod "+h1+"\n")
	}
}

// approve records the Owner's signed approval of deps.
func (f *fixture) approve(t *testing.T, deps []Module, tweak func(*approvals.Approval)) {
	t.Helper()
	signer := f.tr.signer(t, delegation.PurposeApproval, delegation.HolderApprovalService)
	subject := NewDependencySubject(deps)
	a := approvals.Approval{
		RequestID: "req-1", FactoryID: testFactory, Action: ActionApproveDependencies,
		SubjectDigest: subject, PolicyRevision: policySHA, Nonce: "nonce-1", Decision: approvals.DecisionApprove,
		ActorID: "owner", ActorKind: core.ActorHuman, Surface: "dashboard",
		DecidedAt: f.tr.clock.Now(), ExpiresAt: f.tr.clock.Now().Add(time.Hour),
	}
	if tweak != nil {
		tweak(&a)
	}
	sig, err := signer.Sign(context.Background(), a.SigningPayload())
	if err != nil {
		t.Fatalf("Sign approval: %v", err)
	}
	a.Signature = sig
	f.approvals[subject] = a
}

// check runs the gate and fails the test if it could not run.
func (f *fixture) check(t *testing.T) qa.GateResult {
	t.Helper()
	res, err := f.gate.Check(context.Background(), candidate())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Gate != qa.GateSupplyChain {
		t.Fatalf("gate = %s", res.Gate)
	}
	return res
}

func wantPass(t *testing.T, res qa.GateResult) {
	t.Helper()
	if !res.Passed {
		t.Fatalf("gate failed: %s", res.Detail)
	}
}

func wantFail(t *testing.T, res qa.GateResult, detail string) {
	t.Helper()
	if res.Passed {
		t.Fatalf("gate passed, want a failure mentioning %q", detail)
	}
	if !strings.Contains(res.Detail, detail) {
		t.Fatalf("detail = %q, want it to mention %q", res.Detail, detail)
	}
}

func TestGateIsTheSupplyChainHardGate(t *testing.T) {
	t.Parallel()
	var gate qa.HardGate = newFixture(t).gate
	if gate.Name() != qa.GateSupplyChain {
		t.Fatalf("Name = %s", gate.Name())
	}
}

func TestGatePassesAChangeOutsideTheSupplyChain(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.change("internal/app/handler.go", "package app\n")
	f.scans.err = core.NewError(core.CodeNotFound, "no scan needed")
	wantPass(t, f.check(t))
}

func TestGatePassesAPinnedScannedUpdate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.change("go.mod", strings.Replace(baseGoMod, "v3.0.1", "v3.0.2", 1))
	h1, _ := moduleSum("yaml")
	f.change("go.sum", "gopkg.in/yaml.v3 v3.0.2 "+h1+"\n")
	res := f.check(t)
	wantPass(t, res)
	if res.EvidenceRef != "scans/head.json" {
		t.Errorf("evidence = %q", res.EvidenceRef)
	}
}

func TestGateRequiresALockfileHashForAMovedModule(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.change("go.mod", strings.Replace(baseGoMod, "v3.0.1", "v3.0.2", 1))
	wantFail(t, f.check(t), "gopkg.in/yaml.v3@v3.0.2 has no hash in go.sum")
}

func TestGateRefusesUnpinnedGoModules(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ gomod, detail string }{
		"branch version": {strings.Replace(baseGoMod, "v3.0.1", "master", 1), "requirement is not pinned"},
		"local replace":  {baseGoMod + "replace gopkg.in/yaml.v3 => ../yaml\n", "replacement is not pinned"},
		"replace block":  {baseGoMod + "replace (\n\tgopkg.in/yaml.v3 => /src/yaml\n)\n", "replacement is not pinned"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.change("go.mod", tc.gomod)
			wantFail(t, f.check(t), tc.detail)
		})
	}
}

func TestGateRefusesAMalformedLockfile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.change("go.sum", "gopkg.in/yaml.v3 v3.0.1 md5:abc\n")
	wantFail(t, f.check(t), "go.sum:1")
}

func TestGateRequiresACleanCompleteScanOfTheCommit(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		mutate func(*fakeScans)
		detail string
	}{
		"vulnerability found":    {func(s *fakeScans) { s.result.Findings = []Finding{{ID: "GO-2026-0001", Module: "gopkg.in/yaml.v3"}} }, "GO-2026-0001"},
		"scan incomplete":        {func(s *fakeScans) { s.result.Completed = false }, "did not complete"},
		"scan of another commit": {func(s *fakeScans) { s.result.CommitSHA = baseSHA }, "another commit"},
		"no scan":                {func(s *fakeScans) { s.err = core.NewError(core.CodeNotFound, "none") }, "no vulnerability scan"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.change(".github/workflows/ci.yml", "jobs:\n  t:\n    steps:\n      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262\n")
			tc.mutate(f.scans)
			wantFail(t, f.check(t), tc.detail)
		})
	}
}

func TestGateRequiresOwnerApprovalForANewDependency(t *testing.T) {
	t.Parallel()
	added := []Module{{Path: "github.com/evil/pkg", Version: "v1.0.0"}}
	cases := map[string]struct {
		approve func(t *testing.T, f *fixture)
		pass    bool
		detail  string
	}{
		"no approval": {func(*testing.T, *fixture) {}, false, "github.com/evil/pkg@v1.0.0 need the Owner's approval"},
		"approved":    {func(t *testing.T, f *fixture) { f.approve(t, added, nil) }, true, ""},
		"rejected": {func(t *testing.T, f *fixture) {
			f.approve(t, added, func(a *approvals.Approval) { a.Decision = approvals.DecisionReject })
		}, false, "does not authorize"},
		"expired": {func(t *testing.T, f *fixture) {
			f.approve(t, added, nil)
			f.tr.clock.Advance(2 * time.Hour)
		}, false, "does not authorize"},
		"other policy": {func(t *testing.T, f *fixture) {
			f.approve(t, added, func(a *approvals.Approval) { a.PolicyRevision = headSHA })
		}, false, "does not authorize"},
		"by an agent": {func(t *testing.T, f *fixture) {
			f.approve(t, added, func(a *approvals.Approval) { a.ActorKind = core.ActorAgent })
		}, false, "another action, factory or actor"},
		"other action": {func(t *testing.T, f *fixture) {
			f.approve(t, added, func(a *approvals.Approval) { a.Action = "merge" })
		}, false, "another action, factory or actor"},
		"forged signature": {func(t *testing.T, f *fixture) {
			f.approve(t, added, nil)
			a := f.approvals[NewDependencySubject(added)]
			a.ExpiresAt = a.ExpiresAt.Add(24 * time.Hour)
			f.approvals[NewDependencySubject(added)] = a
		}, false, "not validly signed"},
		"approval of another set": {func(t *testing.T, f *fixture) {
			other := []Module{{Path: "github.com/good/pkg", Version: "v1.0.0"}}
			f.approve(t, other, nil)
			f.approvals[NewDependencySubject(added)] = f.approvals[NewDependencySubject(other)]
		}, false, "does not authorize"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.addModule("github.com/evil/pkg", "v1.0.0", true)
			tc.approve(t, f)
			res := f.check(t)
			if tc.pass {
				wantPass(t, res)
				return
			}
			wantFail(t, res, tc.detail)
		})
	}
}

func TestGateRefusesUnpinnedBaseImages(t *testing.T) {
	t.Parallel()
	pinned := "golang:1.25@sha256:" + strings.Repeat("a", 64)
	cases := map[string]struct {
		dockerfile string
		pass       bool
	}{
		"pinned stages":           {"FROM " + pinned + " AS build\nRUN go build\nFROM build AS test\nFROM scratch\nCOPY --from=build /x /x\n", true},
		"platform flag":           {"FROM --platform=linux/amd64 " + pinned + "\n", true},
		"tag only":                {"FROM alpine:3.22\n", false},
		"latest":                  {"from alpine\n", false},
		"variable":                {"ARG BASE=alpine\nFROM ${BASE}\n", false},
		"continuation":            {"FROM \\\n  alpine:3.22\n", false},
		"stage before definition": {"FROM build\nFROM " + pinned + " AS build\n", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.change("build/app.Dockerfile", tc.dockerfile)
			res := f.check(t)
			if tc.pass {
				wantPass(t, res)
				return
			}
			wantFail(t, res, "build/app.Dockerfile")
		})
	}
}

func TestGateRefusesUnpinnedCIConfiguration(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		workflow string
		pass     bool
	}{
		"pinned":        {"steps:\n  - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4\n  - uses: ./.github/actions/local\n", true},
		"tag":           {"steps:\n  - uses: actions/checkout@v4\n", false},
		"branch quoted": {"steps:\n  - uses: \"actions/checkout@main\"\n", false},
		"short sha":     {"steps:\n  - uses: actions/checkout@11d5960\n", false},
		"docker by tag": {"steps:\n  - uses: docker://alpine:3.22\n", false},
		"service image": {"services:\n  db:\n    image: postgres:17\n", false},
		"pinned image":  {"container:\n  image: postgres@sha256:" + strings.Repeat("b", 64) + "\n", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.change(".github/workflows/ci.yml", tc.workflow)
			res := f.check(t)
			if tc.pass {
				wantPass(t, res)
				return
			}
			wantFail(t, res, ".github/workflows/ci.yml")
		})
	}
}

func TestGateFailsClosedOnUncheckableManifests(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.change("web/package.json", `{"dependencies":{"left-pad":"^1.0.0"}}`)
	wantFail(t, f.check(t), "web/package.json: pins of this file kind cannot be verified")
}

func TestGateAllowsADeletedFile(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.tree.changed = append(f.tree.changed, "build/old.Dockerfile")
	wantPass(t, f.check(t))
}

func TestGateBlocksWhenItCannotRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.tree.err = errors.New("git is unavailable")
	if _, err := f.gate.Check(context.Background(), candidate()); err == nil {
		t.Fatal("a gate that could not read the change returned a result")
	}
	f = newFixture(t)
	f.change("go.mod", baseGoMod)
	f.scans.err = core.NewError(core.CodeUnavailable, "scanner down")
	if _, err := f.gate.Check(context.Background(), candidate()); err == nil {
		t.Fatal("a gate that could not read the scan returned a result")
	}
	unbuilt := candidate()
	unbuilt.Digest = ""
	_, err := newFixture(t).gate.Check(context.Background(), unbuilt)
	wantCode(t, err, core.CodeInvalidArgument)
}

func TestNewGateNeedsEverySource(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	full := GateConfig{FactoryID: testFactory, Tree: f.tree, Scans: f.scans, Approvals: f.approvals, ApprovalVerifier: f.tr.verifier, Clock: f.tr.clock}
	cases := map[string]func(*GateConfig){
		"no factory":  func(c *GateConfig) { c.FactoryID = "" },
		"no tree":     func(c *GateConfig) { c.Tree = nil },
		"no scans":    func(c *GateConfig) { c.Scans = nil },
		"no approval": func(c *GateConfig) { c.Approvals = nil },
		"no verifier": func(c *GateConfig) { c.ApprovalVerifier = nil },
		"no clock":    func(c *GateConfig) { c.Clock = nil },
	}
	for name, change := range cases {
		cfg := full
		change(&cfg)
		if _, err := NewGate(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: error = %v, want INVALID_ARGUMENT", name, err)
		}
	}
}

func TestDependencySubjectCoversTheExactSet(t *testing.T) {
	t.Parallel()
	a := Module{Path: "example.com/a", Version: "v1.0.0"}
	b := Module{Path: "example.com/b", Version: "v1.0.0"}
	if NewDependencySubject([]Module{a, b}) != NewDependencySubject([]Module{b, a}) {
		t.Error("the order of the set changed the subject")
	}
	if NewDependencySubject([]Module{a}) == NewDependencySubject([]Module{a, b}) {
		t.Error("a subset has the subject of the whole set")
	}
	if NewDependencySubject([]Module{a}) == NewDependencySubject([]Module{{Path: a.Path, Version: "v1.0.1"}}) {
		t.Error("another version has the same subject")
	}
}

func TestDetectChangesClassifiesSupplyChainPaths(t *testing.T) {
	t.Parallel()
	got := DetectChanges([]string{
		"go.mod", "tools/go.sum", "build/sisf.Dockerfile", "Dockerfile", "deploy/Containerfile.dev",
		".github/workflows/ci.yml", ".github/actions/setup/action.yml", ".gitlab-ci.yml",
		"web/package-lock.json", "requirements-dev.txt", "README.md", "internal/app/go_mod.go", "go.mod",
	})
	want := map[string]ChangeKind{
		".github/actions/setup/action.yml": KindCIConfig, ".github/workflows/ci.yml": KindCIConfig, ".gitlab-ci.yml": KindCIConfig,
		"Dockerfile": KindBaseImage, "build/sisf.Dockerfile": KindBaseImage, "deploy/Containerfile.dev": KindBaseImage,
		"go.mod": KindDependency, "requirements-dev.txt": KindDependency, "tools/go.sum": KindLockfile, "web/package-lock.json": KindLockfile,
	}
	if len(got) != len(want) {
		t.Fatalf("DetectChanges = %+v", got)
	}
	for _, c := range got {
		if want[c.Path] != c.Kind {
			t.Errorf("%s classified as %q, want %q", c.Path, c.Kind, want[c.Path])
		}
	}
}
