package cliagent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// maxLine bounds one stdout line. A longer line is skipped.
	maxLine = 4 << 20
	// maxEvents bounds the events kept per run.
	maxEvents = 5000
	// logTailSize bounds the stderr kept per run.
	logTailSize = 64 << 10
	// drainGrace is how long the output pipes may stay open after the
	// process exits before they are closed on a straggler that left the
	// process group.
	drainGrace = 2 * time.Second
)

type stopReason int

const (
	stopCancelled stopReason = iota + 1
	stopTimedOut
	stopTurnLimit
)

// run is one started process and everything recorded about it.
type run struct {
	b         *Backend
	ref       agent.RunRef
	task      agent.Task
	workspace agent.WorkspaceRef
	scopes    []string
	version   string
	startedAt time.Time

	cmd     *exec.Cmd
	session Session
	stdin   chan []byte
	// stdinOnce closes stdin exactly once.
	stdinOnce sync.Once
	// done is closed when the run has finished and its state is final.
	done chan struct{}

	mu       sync.Mutex
	phase    agent.Phase
	detail   string
	updated  time.Time
	events   []agent.Event
	dropped  bool
	usage    *Usage
	turns    int
	final    *Outcome
	stopped  stopReason
	exited   bool
	logTails []byte
}

func newRun(b *Backend, ref agent.RunRef, in Input, version string) *run {
	now := b.clock.Now()
	return &run{
		b: b, ref: ref, task: in.Task, workspace: in.Workspace, scopes: in.Scopes, version: version,
		startedAt: now, phase: agent.PhaseRunning, updated: now,
		stdin: make(chan []byte, 8), done: make(chan struct{}),
	}
}

// spawn starts the process and the goroutines that follow it.
func (r *run) spawn(bin string, args, env []string, session Session) error {
	r.session = session
	cmd := exec.Command(bin, args...)
	cmd.Dir = r.workspace.Path
	cmd.Env = env
	isolate(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the agent process could not be started")
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return core.NewError(core.CodeUnavailable, "the agent process could not be started")
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		return core.NewError(core.CodeUnavailable, "the agent process could not be started")
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{outR, outW, errR, errW} {
			_ = f.Close()
		}
		// The cause is redacted: a start error can quote the environment.
		return core.NewError(core.CodeUnavailable, "the agent process could not be started").WithCause(errors.New(r.b.redactor.Text(err.Error())))
	}
	_ = outW.Close()
	_ = errW.Close()
	r.cmd = cmd

	r.mu.Lock()
	r.appendLocked(agent.EventStatus, map[string]any{
		"phase": agent.PhaseRunning, "tool": r.b.proto.Tool(), "tool_version": r.version,
		"model": r.task.Policy.ModelRoute.String(),
	})
	r.mu.Unlock()

	// The limit is armed before the goroutines start, so a clock advanced
	// right after Start cannot be missed.
	limit := r.b.clock.After(r.task.Limits.Timeout)
	go r.writeInput(stdin)
	r.apply(session.Begin())
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); r.readOutput(outR) }()
	go func() { defer readers.Done(); r.readLog(errR) }()
	go func() {
		select {
		case <-limit:
			r.stop(stopTimedOut)
		case <-r.done:
		}
	}()
	go r.supervise(&readers, outR, errR)
	return nil
}

// supervise waits for the process, drains its output and records the result.
func (r *run) supervise(readers *sync.WaitGroup, outR, errR *os.File) {
	waitErr := r.cmd.Wait()
	r.mu.Lock()
	r.exited = true
	r.mu.Unlock()
	// Sweep what the agent left behind, such as a background server. The
	// group id is the leader's pid, which cannot have been reused within
	// the instant since it was reaped.
	_ = killGroup(r.cmd)
	r.closeStdin()

	drained := make(chan struct{})
	go func() { readers.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(drainGrace):
		// A descendant that left the group still holds the output; stop
		// waiting for it.
		_ = outR.Close()
		_ = errR.Close()
		<-drained
	}
	_ = outR.Close()
	_ = errR.Close()
	r.finish(waitErr)
	r.b.remember(r.ref.ID)
}

// stop kills the process group once, unless the process is already gone.
func (r *run) stop(reason stopReason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exited || r.stopped != 0 {
		return
	}
	r.stopped = reason
	_ = killGroup(r.cmd)
}

// finish records the final phase from how the process ended.
func (r *run) finish(waitErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	phase, detail := agent.PhaseFailed, ""
	switch {
	case r.stopped == stopCancelled:
		phase, detail = agent.PhaseCancelled, "cancelled"
	case r.stopped == stopTurnLimit:
		detail = fmt.Sprintf("the run exceeded its limit of %d turns and was killed", r.task.Limits.MaxTurns)
	case r.stopped == stopTimedOut:
		detail = fmt.Sprintf("the run exceeded its time limit of %s and was killed", r.task.Limits.Timeout)
	case r.final != nil && !r.final.OK:
		detail = r.final.Detail
	case waitErr != nil:
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			detail = "the agent process exited with status " + strconv.Itoa(exit.ExitCode())
		} else {
			detail = "the agent process ended abnormally"
		}
	case r.final == nil:
		detail = "the agent process exited without a final report"
	default:
		phase = agent.PhaseSucceeded
	}
	r.phase, r.detail, r.updated = phase, r.b.redactor.Text(detail), r.b.clock.Now()
	kind := agent.EventStatus
	if phase == agent.PhaseFailed {
		kind = agent.EventError
	}
	r.appendLocked(kind, map[string]any{"phase": phase, "detail": r.detail})
	close(r.done)
}

// readOutput feeds stdout lines to the protocol session.
func (r *run) readOutput(f *os.File) {
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		line, over, err := readLine(br, maxLine)
		if over {
			r.mu.Lock()
			r.appendLocked(agent.EventError, map[string]any{"detail": "an output line over the size limit was skipped"})
			r.mu.Unlock()
		}
		if len(line) > 0 {
			r.apply(r.session.Line(line))
		}
		if err != nil {
			return
		}
	}
}

// readLog keeps the tail of stderr.
func (r *run) readLog(f *os.File) {
	buf := make([]byte, 8<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			r.mu.Lock()
			r.logTails = append(r.logTails, buf[:n]...)
			if over := len(r.logTails) - logTailSize; over > 0 {
				r.logTails = slices.Clone(r.logTails[over:])
			}
			r.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// writeInput writes queued input to the process and closes it at the end.
func (r *run) writeInput(w io.WriteCloser) {
	defer func() { _ = w.Close() }()
	broken := false
	for b := range r.stdin {
		if broken {
			continue
		}
		if _, err := w.Write(b); err != nil {
			// The process is gone or no longer reads; drain the queue.
			broken = true
		}
	}
}

func (r *run) send(b []byte) {
	select {
	case r.stdin <- b:
	case <-r.done:
	}
}

func (r *run) closeStdin() { r.stdinOnce.Do(func() { close(r.stdin) }) }

// apply records one protocol update.
func (r *run) apply(u Update) {
	r.mu.Lock()
	for _, e := range u.Events {
		r.appendLocked(e.Kind, e.Payload)
	}
	if u.Usage != nil {
		r.usage = u.Usage
	}
	if u.Final != nil && r.final == nil {
		r.final = u.Final
	}
	overTurns := false
	if u.Turn {
		r.turns++
		overTurns = r.task.Limits.MaxTurns > 0 && r.turns > r.task.Limits.MaxTurns
	}
	r.mu.Unlock()
	if overTurns {
		r.stop(stopTurnLimit)
	}
	if len(u.Stdin) > 0 {
		r.send(u.Stdin)
	}
	if u.CloseStdin {
		r.closeStdin()
	}
}

// appendLocked adds a redacted event. Past the cap events are dropped, once
// noted, so a chatty tool cannot exhaust memory.
func (r *run) appendLocked(kind agent.EventKind, payload any) {
	if len(r.events) >= maxEvents {
		if !r.dropped {
			r.dropped = true
			r.events = append(r.events, r.eventLocked(agent.EventError, map[string]any{"detail": "the event limit was reached; later events are not kept"}))
		}
		return
	}
	r.events = append(r.events, r.eventLocked(kind, payload))
}

func (r *run) eventLocked(kind agent.EventKind, payload any) agent.Event {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{"detail":"an event payload could not be encoded"}`)
	}
	return agent.Event{
		Cursor: agent.Cursor(strconv.Itoa(len(r.events) + 1)), At: r.b.clock.Now(), Kind: kind,
		Payload: json.RawMessage(r.b.redactor.Bytes(raw)),
	}
}

func (r *run) status() agent.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return agent.Status{Phase: r.phase, Detail: r.detail, UpdatedAt: r.updated}
}

func (r *run) eventsAfter(from int) agent.EventPage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if from > len(r.events) {
		from = len(r.events)
	}
	return agent.EventPage{Events: slices.Clone(r.events[from:]), Next: agent.Cursor(strconv.Itoa(len(r.events)))}
}

func (r *run) logTail() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.b.redactor.Bytes(r.logTails)
}

func (r *run) usageRecords() []core.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.usage == nil {
		return nil
	}
	at := r.updated
	return []core.Usage{{
		IssueID: r.task.IssueID, RunID: r.task.RunID, Step: r.task.Step, ModelRoute: r.task.Policy.ModelRoute,
		InputTokens: r.usage.InputTokens, OutputTokens: r.usage.OutputTokens, Cost: r.usage.Cost,
		Verified: false, At: at,
	}}
}

// readLine reads one line of at most limit bytes. A longer line is consumed
// and reported through over, with no content.
func readLine(br *bufio.Reader, limit int) (line []byte, over bool, err error) {
	var buf []byte
	for {
		frag, ferr := br.ReadSlice('\n')
		if !over {
			if len(buf)+len(frag) > limit {
				over, buf = true, nil
			} else {
				buf = append(buf, frag...)
			}
		}
		switch {
		case ferr == nil:
			return trimEOL(buf), over, nil
		case errors.Is(ferr, bufio.ErrBufferFull):
			continue
		default:
			return trimEOL(buf), over, ferr
		}
	}
}

func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
