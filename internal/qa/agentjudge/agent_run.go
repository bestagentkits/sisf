package agentjudge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// agentRun is a finished, successful QA agent run.
type agentRun struct {
	ref agent.RunRef
	// final is the text of the agent's last message: the structured
	// channel. Tool output, earlier messages and the log are never read.
	final string
}

// run starts the agent in the workspace and waits for it within the
// judge's time limit. A run that hangs is cancelled and reported as
// TIMEOUT; one that fails or exits non-zero as UNAVAILABLE.
func (j *Judge) run(ctx context.Context, task agent.Task, workspace string) (agentRun, error) {
	ref, err := j.cfg.Agent.Start(ctx, task, agent.WorkspaceRef{ID: "qa-" + filepath.Base(workspace), Path: workspace}, nil)
	if err != nil {
		return agentRun{}, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, j.cfg.Timeout)
	defer cancel()
	status, err := j.cfg.Agent.Wait(waitCtx, ref)
	if err != nil {
		j.stop(ref)
		// What the run spent is recorded even though it gave no judgment.
		_ = j.recordUsage(context.WithoutCancel(ctx), ref)
		if ctx.Err() != nil {
			return agentRun{}, core.NewError(core.CodeUnavailable, "the judgment was interrupted").WithCause(ctx.Err())
		}
		return agentRun{}, core.NewError(core.CodeTimeout, "the QA agent did not finish within "+j.cfg.Timeout.String())
	}
	usageErr := j.recordUsage(ctx, ref)
	if status.Phase != agent.PhaseSucceeded {
		// The detail is the adapter's, already redacted.
		return agentRun{}, core.NewError(core.CodeUnavailable, "the QA agent run "+string(status.Phase)+": "+status.Detail)
	}
	if usageErr != nil {
		return agentRun{}, usageErr
	}
	final, err := j.finalMessage(ctx, ref)
	if err != nil {
		return agentRun{}, err
	}
	return agentRun{ref: ref, final: final}, nil
}

// stop cancels a run that outlived its limit and waits briefly for it.
func (j *Judge) stop(ref agent.RunRef) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelGrace)
	defer cancel()
	_ = j.cfg.Agent.Cancel(ctx, ref)
}

// finalMessage returns the text of the run's last message event.
func (j *Judge) finalMessage(ctx context.Context, ref agent.RunRef) (string, error) {
	page, err := j.cfg.Agent.Events(ctx, ref, "")
	if err != nil {
		return "", err
	}
	final := ""
	for _, e := range page.Events {
		if e.Kind != agent.EventMessage {
			continue
		}
		var p struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(e.Payload, &p) == nil {
			final = p.Text
		}
	}
	return final, nil
}

// recordUsage passes what the adapter reports the run consumed to the
// usage sink. An adapter that cannot report is not an error; the figures
// are the tool's own and stay unverified, with a cost of zero where the
// tool reports none.
func (j *Judge) recordUsage(ctx context.Context, ref agent.RunRef) error {
	reporter, ok := j.cfg.Agent.(agent.UsageReporter)
	if j.cfg.Usage == nil || !ok {
		return nil
	}
	usage, err := reporter.Usage(ctx, ref)
	if core.HasCode(err, core.CodeNotSupported) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, u := range usage {
		u.FactoryID, u.Verified = j.cfg.FactoryID, false
		if err := j.cfg.Usage.AppendUsage(ctx, u); err != nil {
			return err
		}
	}
	return nil
}

// evidence is the record a judgment's scores cite.
type evidence struct {
	IssueID    core.IssueRef    `json:"issue_id"`
	RunID      core.RunID       `json:"run_id"`
	Attempt    int              `json:"attempt"`
	Commit     core.CommitSHA   `json:"commit_sha"`
	Identity   string           `json:"qa_identity"`
	ModelRoute core.ModelRoute  `json:"model_route"`
	Agent      agent.RunRef     `json:"agent_run"`
	Artifacts  []agent.Artifact `json:"artifacts,omitempty"`
	Judgment   Judgment         `json:"judgment"`
	RecordedAt time.Time        `json:"recorded_at"`
}

// keep writes the judgment and where the run's transcript is to the
// evidence directory and returns a reference that names its digest.
func (j *Judge) keep(ctx context.Context, run agentRun, req qa.JudgeRequest, route core.ModelRoute, judgment Judgment) (string, error) {
	artifacts, err := j.cfg.Agent.CollectArtifacts(ctx, run.ref)
	if err != nil {
		return "", err
	}
	record := evidence{
		IssueID: req.Candidate.IssueID, RunID: req.Candidate.RunID, Attempt: req.Candidate.Attempt, Commit: req.Candidate.CommitSHA,
		Identity: req.Identity, ModelRoute: route, Agent: run.ref, Artifacts: artifacts, Judgment: judgment, RecordedAt: j.cfg.Clock.Now().UTC(),
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", core.NewError(core.CodeInternal, "the judgment evidence cannot be encoded").WithCause(err)
	}
	if err := os.MkdirAll(j.cfg.EvidenceDir, 0o700); err != nil {
		return "", core.NewError(core.CodeUnavailable, "the evidence directory cannot be made").WithCause(err)
	}
	name := filepath.Join(j.cfg.EvidenceDir, filepath.Base(string(req.Candidate.RunID))+"-"+itoa(req.Candidate.Attempt)+"-"+filepath.Base(run.ref.ID)+".json")
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		return "", core.NewError(core.CodeUnavailable, "the judgment evidence cannot be written").WithCause(err)
	}
	sum := sha256.Sum256(raw)
	return "file://" + filepath.ToSlash(name) + "#sha256:" + hex.EncodeToString(sum[:]), nil
}
