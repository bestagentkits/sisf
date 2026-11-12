package cliagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/redact"
	"github.com/bestagentkits/sisf/pkg/core"
)

// maxFinishedRuns bounds how many finished runs a backend remembers.
const maxFinishedRuns = 256

// Config builds a Backend.
type Config struct {
	// Protocol is the tool-specific part. Required.
	Protocol Protocol
	// Name is the backend name in run references; the tool name when empty.
	Name string
	// Binary is the program to run; the tool name when empty. A bare name is
	// looked up on the PATH of Env, not on the PATH of this process.
	Binary string
	// Env is the complete environment of the tool, KEY=VALUE. It is the only
	// way credentials reach the tool. Nil or empty is an empty environment:
	// the tool never inherits this process's environment, which holds the
	// secrets of whatever starts it. Build one with package agentenv.
	Env []string
	// Clock drives the time limit; the wall clock when nil.
	Clock core.Clock
	// ArtifactDir is where artifacts are written; a private temporary
	// directory, created on first use, when empty.
	ArtifactDir string
	// Secrets are extra exact values to redact, besides those found in Env.
	Secrets []string
}

// Backend is an agent.Backend that runs a local CLI. It is safe for
// concurrent use.
type Backend struct {
	name     string
	proto    Protocol
	binary   string
	env      []string
	clock    core.Clock
	redactor *redact.Redactor
	// drainGrace is the silence after a run's exit that closes its output
	// pipes on a straggler; see run.drain.
	drainGrace time.Duration

	artifactMu  sync.Mutex
	artifactDir string

	versionMu sync.Mutex
	versions  map[string]string

	mu       sync.Mutex
	runs     map[string]*run
	byKey    map[string]string
	finished []string
	seq      int
}

var (
	_ agent.Backend         = (*Backend)(nil)
	_ agent.Resumer         = (*Backend)(nil)
	_ agent.UsageReporter   = (*Backend)(nil)
	_ agent.RunInfoProvider = (*Backend)(nil)
	// The remaining optional abilities are answered NOT_SUPPORTED.
	_ agent.Checkpointer       = (*Backend)(nil)
	_ agent.HumanInputReceiver = (*Backend)(nil)
	_ agent.SessionLinker      = (*Backend)(nil)
	_ agent.WebhookHandler     = (*Backend)(nil)
)

// New builds a backend around a protocol.
func New(cfg Config) (*Backend, error) {
	if cfg.Protocol == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "a CLI backend needs a protocol")
	}
	b := &Backend{
		name: cfg.Name, proto: cfg.Protocol, binary: cfg.Binary, clock: cfg.Clock,
		artifactDir: cfg.ArtifactDir, versions: map[string]string{}, drainGrace: drainGrace,
		runs: map[string]*run{}, byKey: map[string]string{},
	}
	if b.name == "" {
		b.name = cfg.Protocol.Tool()
	}
	if b.binary == "" {
		b.binary = cfg.Protocol.Tool()
	}
	if b.clock == nil {
		b.clock = core.SystemClock{}
	}
	b.env = cleanEnv(cfg.Env)
	b.redactor = redact.New(b.env, cfg.Secrets...)
	return b, nil
}

// Name returns the backend name.
func (b *Backend) Name() string { return b.name }

// Capabilities declares what the backend does. Cancel, resume from a handoff
// and usage reporting are supported. Replay is not, because events live only
// as long as this process. Checkpoint, human input, a session link and
// webhooks do not exist for a one-shot CLI run. A hard budget is not
// supported: any limit the tool enforces is its own, not the provider's.
// It reports UNAVAILABLE when the program is not installed.
func (b *Backend) Capabilities(context.Context) (core.CapabilitySet, error) {
	if _, err := findBinary(b.binary, b.env); err != nil {
		return nil, err
	}
	return core.CapabilitySet{
		core.CapAgentCancel:      core.SupportSupported,
		core.CapAgentResume:      core.SupportSupported,
		core.CapAgentUsage:       core.SupportSupported,
		core.CapAgentReplay:      core.SupportNotSupported,
		core.CapAgentCheckpoint:  core.SupportNotSupported,
		core.CapAgentHumanInput:  core.SupportNotSupported,
		core.CapAgentSessionLink: core.SupportNotSupported,
		core.CapAgentWebhooks:    core.SupportNotSupported,
		core.CapAgentHardBudget:  core.SupportNotSupported,
	}, nil
}

// Start begins a run. A task repeated with the same idempotency key returns
// the run it started the first time.
func (b *Backend) Start(ctx context.Context, task agent.Task, ws agent.WorkspaceRef, scopes []string) (agent.RunRef, error) {
	if err := task.Validate(); err != nil {
		return agent.RunRef{}, err
	}
	if err := validateWorkspace(ws); err != nil {
		return agent.RunRef{}, err
	}
	if err := validateScopes(scopes); err != nil {
		return agent.RunRef{}, err
	}
	if ref, found, err := b.existing(task); found {
		return ref, err
	}
	in := Input{Task: task, Workspace: ws, Scopes: slices.Clone(scopes), Prompt: RenderPrompt(task)}
	inv, err := b.proto.Invocation(in)
	if err != nil {
		return agent.RunRef{}, err
	}
	env := append(slices.Clone(b.env), cleanEnv(inv.Env)...)
	bin, err := findBinary(b.binary, env)
	if err != nil {
		return agent.RunRef{}, err
	}
	version, err := b.toolVersion(ctx, bin, env)
	if err != nil {
		return agent.RunRef{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if ref, found, err := b.existingLocked(task); found {
		return ref, err
	}
	b.seq++
	id := b.name + "-" + strconv.Itoa(b.seq)
	r := newRun(b, agent.RunRef{Backend: b.name, ID: id, RunID: task.RunID}, in, version)
	if err := r.spawn(bin, inv.Args, env, b.proto.NewSession(in)); err != nil {
		return agent.RunRef{}, err
	}
	b.runs[id] = r
	b.byKey[task.IdempotencyKey] = id
	return r.ref, nil
}

func (b *Backend) existing(task agent.Task) (agent.RunRef, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.existingLocked(task)
}

func (b *Backend) existingLocked(task agent.Task) (agent.RunRef, bool, error) {
	id, ok := b.byKey[task.IdempotencyKey]
	if !ok {
		return agent.RunRef{}, false, nil
	}
	first := b.runs[id]
	if first == nil {
		// The run was forgotten; the key can no longer be matched safely.
		return agent.RunRef{}, true, core.NewError(core.CodeConflict, "the idempotency key started a run that is no longer remembered")
	}
	if first.task.RunID != task.RunID || first.task.IssueID != task.IssueID || first.task.Role != task.Role {
		return agent.RunRef{}, true, core.NewError(core.CodeConflict, "the idempotency key already started another task")
	}
	return first.ref, true, nil
}

// toolVersion returns the version the program reports, probed once per
// program path.
func (b *Backend) toolVersion(ctx context.Context, bin string, env []string) (string, error) {
	b.versionMu.Lock()
	defer b.versionMu.Unlock()
	if v, ok := b.versions[bin]; ok {
		return v, nil
	}
	v, err := probeVersion(ctx, bin, env, b.redactor)
	if err != nil {
		return "", err
	}
	b.versions[bin] = v
	return v, nil
}

// remember records a finished run for eviction, dropping the oldest ones.
func (b *Backend) remember(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finished = append(b.finished, id)
	for len(b.finished) > maxFinishedRuns {
		old := b.finished[0]
		b.finished = b.finished[1:]
		delete(b.runs, old)
	}
}

// find returns the run a reference names, or NOT_FOUND.
func (b *Backend) find(ref agent.RunRef) (*run, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.runs[ref.ID]
	if !ok || ref.Backend != b.name || ref.RunID != r.ref.RunID {
		return nil, core.NewError(core.CodeNotFound, "the "+b.name+" backend has no such run")
	}
	return r, nil
}

// Status reports where the run stands.
func (b *Backend) Status(_ context.Context, ref agent.RunRef) (agent.Status, error) {
	r, err := b.find(ref)
	if err != nil {
		return agent.Status{}, err
	}
	return r.status(), nil
}

// Events returns the events after cursor. The cursor is the number of events
// already read.
func (b *Backend) Events(_ context.Context, ref agent.RunRef, cursor agent.Cursor) (agent.EventPage, error) {
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
	return r.eventsAfter(from), nil
}

// Cancel kills the run's process group and waits for the process to be gone.
// Cancelling a finished run changes nothing.
func (b *Backend) Cancel(ctx context.Context, ref agent.RunRef) error {
	r, err := b.find(ref)
	if err != nil {
		return err
	}
	r.stop(stopCancelled)
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return core.NewError(core.CodeTimeout, "the run was killed but its process has not been reaped yet")
	}
}

// Wait blocks until the run has finished and returns its final status. It
// returns TIMEOUT when ctx ends first; the run keeps going.
func (b *Backend) Wait(ctx context.Context, ref agent.RunRef) (agent.Status, error) {
	r, err := b.find(ref)
	if err != nil {
		return agent.Status{}, err
	}
	select {
	case <-r.done:
		return r.status(), nil
	case <-ctx.Done():
		return agent.Status{}, core.NewError(core.CodeTimeout, "the run has not finished yet")
	}
}

// RunInfo reports how the run was executed, including the tool version.
func (b *Backend) RunInfo(_ context.Context, ref agent.RunRef) (agent.RunInfo, error) {
	r, err := b.find(ref)
	if err != nil {
		return agent.RunInfo{}, err
	}
	return agent.RunInfo{
		Backend: b.name, Tool: b.proto.Tool(), ToolVersion: r.version,
		ModelRoute: r.task.Policy.ModelRoute, Scopes: slices.Clone(r.scopes), StartedAt: r.startedAt,
	}, nil
}

// Usage returns what the tool reported the run consumed. The figures are the
// tool's own, so they are never marked verified.
func (b *Backend) Usage(_ context.Context, ref agent.RunRef) ([]core.Usage, error) {
	r, err := b.find(ref)
	if err != nil {
		return nil, err
	}
	return r.usageRecords(), nil
}

// Resume starts a fresh process for the same task from a new handoff. The
// previous run must have ended. Nothing of the previous conversation is
// passed on: the new agent sees only the handoff and the workspace.
func (b *Backend) Resume(ctx context.Context, ref agent.RunRef, handoff agent.Handoff) (agent.RunRef, error) {
	old, err := b.find(ref)
	if err != nil {
		return agent.RunRef{}, err
	}
	if !old.status().Phase.Terminal() {
		return agent.RunRef{}, core.NewError(core.CodeConflict, "the run is still going; cancel it before resuming from a handoff")
	}
	task := old.task
	task.Handoff = handoff
	sum := sha256.Sum256([]byte(strings.Join(append([]string{handoff.SoW, handoff.DoD}, append(slices.Clone(handoff.Decisions), handoff.Evidence...)...), "\x00")))
	task.IdempotencyKey = old.task.IdempotencyKey + "/resume/" + hex.EncodeToString(sum[:6])
	return b.Start(ctx, task, old.workspace, old.scopes)
}

// CollectArtifacts writes the artifacts of a run that succeeded: the
// redacted transcript, the redacted tool log and, for a git workspace, the
// patch of what the agent changed.
func (b *Backend) CollectArtifacts(ctx context.Context, ref agent.RunRef) ([]agent.Artifact, error) {
	r, err := b.find(ref)
	if err != nil {
		return nil, err
	}
	if r.status().Phase != agent.PhaseSucceeded {
		return nil, core.NewError(core.CodeConflict, "the run has not succeeded, so it left nothing to collect")
	}
	root, err := b.artifactRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, ref.ID)
	var out []agent.Artifact
	add := func(name string, kind agent.ArtifactKind, data []byte) error {
		a, err := writeArtifact(dir, name, kind, data)
		if err == nil {
			out = append(out, a)
		}
		return err
	}
	if r.task.Role == agent.RoleCoder {
		patch, err := collectPatch(ctx, r.workspace, root)
		if err != nil {
			return nil, err
		}
		if patch != nil {
			if err := add("patch.diff", agent.ArtifactPatch, patch); err != nil {
				return nil, err
			}
		}
	}
	if err := add("transcript.jsonl", agent.ArtifactTranscript, transcript(r.eventsAfter(0).Events)); err != nil {
		return nil, err
	}
	if err := add("agent.log", agent.ArtifactLog, r.logTail()); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *Backend) artifactRoot() (string, error) {
	b.artifactMu.Lock()
	defer b.artifactMu.Unlock()
	if b.artifactDir == "" {
		dir, err := os.MkdirTemp("", "sisf-"+b.name+"-artifacts-")
		if err != nil {
			return "", core.NewError(core.CodeInternal, "the artifact directory could not be created")
		}
		b.artifactDir = dir
	}
	if err := os.MkdirAll(b.artifactDir, 0o700); err != nil {
		return "", core.NewError(core.CodeInternal, "the artifact directory could not be created")
	}
	return b.artifactDir, nil
}

func validateWorkspace(ws agent.WorkspaceRef) error {
	if ws.Path == "" || !filepath.IsAbs(ws.Path) {
		return core.NewError(core.CodeInvalidArgument, "the workspace needs an absolute path")
	}
	if info, err := os.Stat(ws.Path); err != nil || !info.IsDir() {
		return core.NewError(core.CodeInvalidArgument, "the workspace path is not a directory")
	}
	if ws.BaseSHA != "" {
		return ws.BaseSHA.Validate()
	}
	return nil
}

func validateScopes(scopes []string) error {
	for _, s := range scopes {
		if s == "" || len(s) > 128 || strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return core.NewError(core.CodeInvalidArgument, "a scope must be 1-128 characters with no white space")
		}
	}
	return nil
}

// notSupported is the answer for an ability a CLI run does not have.
func notSupported(what string) error {
	return core.NewError(core.CodeNotSupported, "this backend does not support "+what)
}

// Checkpoint is not supported: a CLI run has no point to return to.
func (b *Backend) Checkpoint(context.Context, agent.RunRef) (string, error) {
	return "", notSupported("checkpoints")
}

// HumanInput is not supported: the run takes all its input at the start.
func (b *Backend) HumanInput(context.Context, agent.RunRef, string) error {
	return notSupported("human input")
}

// SessionLink is not supported: there is no live session to link to.
func (b *Backend) SessionLink(context.Context, agent.RunRef) (string, error) {
	return "", notSupported("session links")
}

// HandleWebhook is not supported: a local CLI sends no callbacks.
func (b *Backend) HandleWebhook(context.Context, http.Header, []byte) (agent.RunRef, []agent.Event, error) {
	return agent.RunRef{}, nil, notSupported("webhooks")
}
