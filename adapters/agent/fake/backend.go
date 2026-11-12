package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// Config builds a backend. Every field is optional.
type Config struct {
	// Name is the backend name in run references; "fake" when empty.
	Name string
	// Clock decides when a scripted run finishes; the wall clock when nil.
	Clock core.Clock
	// Roles are the roles the backend serves; only the coder when empty.
	Roles []agent.Role
	// Default is followed by a run with no script left; a patch that is
	// ready at once when its outcome is unset.
	Default Step
}

// Backend is the scripted agent backend. It is safe for concurrent use.
type Backend struct {
	name  string
	clock core.Clock
	roles []agent.Role
	deflt Step

	mu      sync.Mutex
	scripts map[scriptKey][]Step
	runs    map[string]*run
	// byKey maps a task idempotency key to the run it started.
	byKey   map[string]string
	started []agent.Task
	// judged records every judgment asked of the evaluator role.
	judged []qa.JudgeRequest
}

type scriptKey struct {
	role  agent.Role
	issue core.IssueRef
}

// run is one started run and the script it follows.
type run struct {
	ref         agent.RunRef
	task        agent.Task
	step        Step
	startedAt   time.Time
	cancelledAt time.Time
}

var _ agent.Backend = (*Backend)(nil)

// New builds a backend, refusing an unknown role or an unusable default.
func New(cfg Config) (*Backend, error) {
	b := &Backend{
		name: cfg.Name, clock: cfg.Clock, roles: slices.Clone(cfg.Roles), deflt: cfg.Default,
		scripts: map[scriptKey][]Step{}, runs: map[string]*run{}, byKey: map[string]string{},
	}
	if b.name == "" {
		b.name = "fake"
	}
	if b.clock == nil {
		b.clock = core.SystemClock{}
	}
	if len(b.roles) == 0 {
		b.roles = []agent.Role{agent.RoleCoder}
	}
	for _, role := range b.roles {
		if !role.Valid() {
			return nil, core.NewError(core.CodeInvalidArgument, fmt.Sprintf("the fake backend cannot serve role %q", role))
		}
	}
	if b.deflt.Outcome == "" {
		b.deflt = Patch(0, "")
	}
	if err := b.deflt.validate(); err != nil {
		return nil, err
	}
	return b, nil
}

// Script queues steps for the next runs started for role on issue, in order.
// A run started when the queue is empty follows the default step.
func (b *Backend) Script(role agent.Role, issue core.IssueRef, steps ...Step) error {
	for _, step := range steps {
		if err := step.validate(); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := scriptKey{role, issue}
	b.scripts[key] = append(b.scripts[key], steps...)
	return nil
}

// Started returns the tasks that started a new run for issue, oldest first.
// A Start repeated with the same idempotency key is not counted again.
func (b *Backend) Started(issue core.IssueRef) []agent.Task {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []agent.Task
	for _, task := range b.started {
		if task.IssueID == issue {
			out = append(out, task)
		}
	}
	return out
}

// Name returns the backend name.
func (b *Backend) Name() string { return b.name }

// Capabilities declares cancellation as supported and nothing else: the fake
// cannot resume, replay, meter usage or enforce a budget.
func (b *Backend) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{core.CapAgentCancel: core.SupportSupported}, nil
}

// Start begins a run for a valid task of a served role. A task repeated with
// the same idempotency key returns the run it started the first time.
func (b *Backend) Start(_ context.Context, task agent.Task, _ agent.WorkspaceRef, _ []string) (agent.RunRef, error) {
	if err := task.Validate(); err != nil {
		return agent.RunRef{}, err
	}
	if !slices.Contains(b.roles, task.Role) {
		return agent.RunRef{}, core.NewError(core.CodeNotSupported, fmt.Sprintf("the fake backend does not serve the %s role", task.Role))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if id, ok := b.byKey[task.IdempotencyKey]; ok {
		first := b.runs[id]
		if first.task.RunID != task.RunID || first.task.IssueID != task.IssueID || first.task.Role != task.Role {
			return agent.RunRef{}, core.NewError(core.CodeConflict, "the idempotency key already started another task")
		}
		return first.ref, nil
	}
	key := scriptKey{task.Role, task.IssueID}
	step := b.deflt
	if queue := b.scripts[key]; len(queue) > 0 {
		step, b.scripts[key] = queue[0], queue[1:]
	}
	id := b.name + "-" + strconv.Itoa(len(b.runs)+1)
	r := &run{
		ref:  agent.RunRef{Backend: b.name, ID: id, RunID: task.RunID},
		task: task, step: step, startedAt: b.clock.Now(),
	}
	b.runs[id] = r
	b.byKey[task.IdempotencyKey] = id
	b.started = append(b.started, task)
	return r.ref, nil
}

// Status reports where the run stands now. A crashed run that is due
// answers UNAVAILABLE.
func (b *Backend) Status(_ context.Context, ref agent.RunRef) (agent.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, err := b.find(ref)
	if err != nil {
		return agent.Status{}, err
	}
	return r.status(b.clock.Now())
}

// Events returns the status changes of the run after cursor. The cursor is
// the number of events already read.
func (b *Backend) Events(_ context.Context, ref agent.RunRef, cursor agent.Cursor) (agent.EventPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, err := b.find(ref)
	if err != nil {
		return agent.EventPage{}, err
	}
	from := 0
	if cursor != "" {
		if from, err = strconv.Atoi(string(cursor)); err != nil || from < 0 {
			return agent.EventPage{}, core.NewError(core.CodeInvalidArgument, "the event cursor is not one this backend issued")
		}
	}
	all := r.events(b.clock.Now())
	if from > len(all) {
		from = len(all)
	}
	return agent.EventPage{Events: all[from:], Next: agent.Cursor(strconv.Itoa(len(all)))}, nil
}

// Cancel stops a run that has not finished. Cancelling a finished or lost
// run changes nothing.
func (b *Backend) Cancel(_ context.Context, ref agent.RunRef) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, err := b.find(ref)
	if err != nil {
		return err
	}
	now := b.clock.Now()
	if status, err := r.status(now); err != nil || status.Phase.Terminal() {
		return nil
	}
	r.cancelledAt = now
	return nil
}

// CollectArtifacts returns the patch of a run that succeeded. Any other run
// has nothing to collect yet, which is a CONFLICT, and a lost run is
// UNAVAILABLE.
func (b *Backend) CollectArtifacts(_ context.Context, ref agent.RunRef) ([]agent.Artifact, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, err := b.find(ref)
	if err != nil {
		return nil, err
	}
	status, err := r.status(b.clock.Now())
	if err != nil {
		return nil, err
	}
	if status.Phase != agent.PhaseSucceeded {
		return nil, core.NewError(core.CodeConflict, "the run has not succeeded, so it left no patch")
	}
	name, kind, content := "patch.diff", agent.ArtifactPatch, []byte(r.step.Diff)
	if r.step.Outcome == OutcomeScore {
		name, kind, content = scoreReportName, agent.ArtifactTestReport, scoreReport(r.step.Points)
	}
	sum := sha256.Sum256(content)
	return []agent.Artifact{{
		Name:   name,
		Kind:   kind,
		Digest: core.Digest("sha256:" + hex.EncodeToString(sum[:])),
		URI:    "fake://" + b.name + "/" + r.ref.ID + "/" + name,
		Size:   int64(len(content)),
	}}, nil
}

// find returns the run a reference names, or NOT_FOUND.
func (b *Backend) find(ref agent.RunRef) (*run, error) {
	r, ok := b.runs[ref.ID]
	if !ok || ref.Backend != b.name || ref.RunID != r.ref.RunID {
		return nil, core.NewError(core.CodeNotFound, "the fake backend started no such run")
	}
	return r, nil
}

// due reports whether the scripted outcome shows at now.
func (r *run) due(now time.Time) bool {
	return r.step.Outcome != OutcomeHang && !now.Before(r.startedAt.Add(r.step.After))
}

// status is the run's state at now.
func (r *run) status(now time.Time) (agent.Status, error) {
	if !r.cancelledAt.IsZero() {
		return agent.Status{Phase: agent.PhaseCancelled, Detail: "cancelled", UpdatedAt: r.cancelledAt}, nil
	}
	if !r.due(now) {
		return agent.Status{Phase: agent.PhaseRunning, UpdatedAt: r.startedAt}, nil
	}
	finishedAt := r.startedAt.Add(r.step.After)
	switch r.step.Outcome {
	case OutcomePatch, OutcomeScore:
		return agent.Status{Phase: agent.PhaseSucceeded, UpdatedAt: finishedAt}, nil
	case OutcomeFail:
		return agent.Status{Phase: agent.PhaseFailed, Detail: r.step.Detail, UpdatedAt: finishedAt}, nil
	}
	return agent.Status{}, core.NewError(core.CodeUnavailable, "the agent process exited without a report")
}

// events are the status changes of the run up to now.
func (r *run) events(now time.Time) []agent.Event {
	out := []agent.Event{statusEvent(0, r.startedAt, agent.EventStatus, agent.PhaseRunning, "")}
	status, err := r.status(now)
	switch {
	case err != nil:
		out = append(out, statusEvent(1, r.startedAt.Add(r.step.After), agent.EventError, "", "the agent process exited without a report"))
	case status.Phase.Terminal():
		out = append(out, statusEvent(1, status.UpdatedAt, agent.EventStatus, status.Phase, status.Detail))
	}
	return out
}

func statusEvent(index int, at time.Time, kind agent.EventKind, phase agent.Phase, detail string) agent.Event {
	payload, _ := json.Marshal(struct {
		Phase  agent.Phase `json:"phase,omitempty"`
		Detail string      `json:"detail,omitempty"`
	}{phase, detail})
	return agent.Event{Cursor: agent.Cursor(strconv.Itoa(index + 1)), At: at, Kind: kind, Payload: payload}
}
