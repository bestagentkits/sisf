package local

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy/healthprobe"
)

// maxCriticalMarks bounds the memory of the critical-line record.
const maxCriticalMarks = 10000

// stopGrace is how long a process gets to leave after SIGTERM.
const stopGrace = 5 * time.Second

// ExecStarter starts real operating-system processes.
type ExecStarter struct {
	// Command maps an artifact image to the program and arguments to run.
	// Required; it is where an unknown or unsafe image is refused.
	Command func(image string) (path string, args []string, err error)
	// AddrEnv is the variable that carries the address to listen on.
	AddrEnv string
	// Dir is the working directory of the process.
	Dir string
	// Env is the whole base environment. The host's environment is not
	// inherited, so a secret of the supervisor never reaches the unit.
	Env []string
	// OnLog receives each stderr line, for forwarding or inspection.
	OnLog func(line string)
	// Clock stamps critical lines and times the stop grace; the wall clock when nil.
	Clock core.Clock
}

type execProcess struct {
	cmd    *exec.Cmd
	clock  core.Clock
	done   chan struct{} // closed when the process has exited and its output is read
	mu     sync.Mutex
	marks  []time.Time
	waited error
}

// Start runs the command. The process outlives ctx; Stop ends it.
func (s ExecStarter) Start(_ context.Context, spec StartSpec) (Process, error) {
	if s.Command == nil || s.AddrEnv == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "the starter needs a command resolver and the name of the address variable")
	}
	path, args, err := s.Command(spec.Image)
	if err != nil {
		return nil, err
	}
	clock := s.Clock
	if clock == nil {
		clock = core.SystemClock{}
	}
	cmd := exec.Command(path, args...) //nolint:gosec // the path comes from the configured resolver, never from the artifact text directly
	cmd.Dir = s.Dir
	cmd.Env = append(append(append([]string(nil), s.Env...), spec.Env...), s.AddrEnv+"="+spec.Addr)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", path, err)
	}
	p := &execProcess{cmd: cmd, clock: clock, done: make(chan struct{})}
	go func() {
		p.scan(stderr, s.OnLog)
		p.waited = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// scan reads the process's stderr to its end, recording critical lines.
func (p *execProcess) scan(r io.Reader, onLog func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if healthprobe.IsCriticalLine(line) {
			p.mu.Lock()
			if len(p.marks) < maxCriticalMarks {
				p.marks = append(p.marks, p.clock.Now())
			}
			p.mu.Unlock()
		}
		if onLog != nil {
			onLog(line)
		}
	}
	_, _ = io.Copy(io.Discard, r) // an over-long line must not block the process
}

func (p *execProcess) Alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// Stop asks the process to end and kills it when it does not, or when ctx ends.
func (p *execProcess) Stop(ctx context.Context) error {
	if !p.Alive() {
		return nil
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = p.cmd.Process.Kill() // no SIGTERM on this platform
	}
	select {
	case <-p.done:
		return nil
	case <-p.clock.After(stopGrace):
	case <-ctx.Done():
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-p.done
	return nil
}

func (p *execProcess) CriticalSince(t time.Time) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var n int64
	for _, m := range p.marks {
		if !m.Before(t) {
			n++
		}
	}
	return n
}
