//go:build unix

package agentjudge_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
)

// With a model of its own, the QA agent runs that model and the signed
// receipt names it, not the policy route.
func TestTheReceiptNamesTheModelTheQAAgentRan(t *testing.T) {
	t.Parallel()
	s := newJudge(t, codexRun(codexMessage("m1", block(wireJudgment(26)))), func(c *agentjudge.Config) { c.Model = "gpt-judge" })
	result, err := newEvaluator(t, s.judge, false).Run(context.Background(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	want := core.ModelRoute{Provider: "openai", Model: "gpt-judge", Effort: "low"}
	if result.Receipt.QAModelRoute != want || result.Receipt.SameModelRoute || result.Receipt.Signature.IsZero() {
		t.Fatalf("the receipt records %+v, want %+v", result.Receipt.QAModelRoute, want)
	}
	if args := clitest.Recorded(t, s.record).Args; !slices.Contains(args, "gpt-judge") || slices.Contains(args, "mid") {
		t.Fatalf("the QA agent ran %v", args)
	}
}

// The evidence a receipt cites holds the judgment and references only:
// whatever the agent read outside its workspace and echoed in tool output
// or earlier messages is not copied into it, and the transcript is named by
// its digest, never inlined.
func TestTheEvidenceHoldsNoContentFromOutsideTheWorkspace(t *testing.T) {
	t.Parallel()
	const secret = "-----BEGIN PRIVATE KEY----- trust-volume-key-material"
	outside := filepath.Join(t.TempDir(), "trust", "qa_receipt.key")
	if err := os.MkdirAll(filepath.Dir(outside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newJudge(t, codexRun(
		clitest.Step{Emit: codexLine(map[string]any{"type": "item.completed", "item": map[string]any{
			"id": "c1", "type": "command_execution", "command": "cat " + outside, "aggregated_output": secret, "exit_code": 0, "status": "completed"}})},
		codexMessage("m0", "I found "+secret),
		codexMessage("m1", block(wireJudgment(26))),
	), nil)
	card, err := s.judge.Score(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	path, _, _ := strings.Cut(strings.TrimPrefix(card.EvidenceRef, "file://"), "#")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "trust-volume-key-material") {
		t.Fatal("the evidence copied what the agent read outside its workspace")
	}
	for _, d := range card.Details {
		if strings.Contains(d, "PRIVATE KEY") {
			t.Fatal("a receipt detail holds what the agent read outside its workspace")
		}
	}
	// Exactly the declared fields; artifacts are named, not inlined.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range doc {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"agent_run", "artifacts", "attempt", "commit_sha", "issue_id", "judgment", "model_route", "qa_identity", "recorded_at", "run_id"}
	if !slices.Equal(keys, want) {
		t.Fatalf("the evidence holds %v, want %v", keys, want)
	}
	var artifacts []map[string]any
	if err := json.Unmarshal(doc["artifacts"], &artifacts); err != nil || len(artifacts) == 0 {
		t.Fatalf("artifacts %s: %v", doc["artifacts"], err)
	}
	for _, a := range artifacts {
		for field := range a {
			if !slices.Contains([]string{"name", "kind", "digest", "uri", "size"}, field) {
				t.Fatalf("an artifact reference carries %q", field)
			}
		}
	}
}
