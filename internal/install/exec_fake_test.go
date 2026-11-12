package install

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// fakeExec records every program asked for and answers from a script, so
// nothing is built or started. Programs started in the background run until
// the test stops them.
type fakeExec struct {
	mu      sync.Mutex
	runs    []Cmd
	started []*fakeProc
	// answer decides the result of a Run; nil means success with no output.
	answer func(Cmd) ([]byte, error)
	// failStart makes Start fail for programs whose name has this suffix.
	failStart string
	// startCh announces each started program, so a test can wait for it
	// without sleeping.
	startCh chan *fakeProc
}

func newFakeExec() *fakeExec { return &fakeExec{startCh: make(chan *fakeProc, 256)} }

func (f *fakeExec) Run(_ context.Context, c Cmd) ([]byte, error) {
	f.mu.Lock()
	f.runs = append(f.runs, c)
	answer := f.answer
	f.mu.Unlock()
	if answer != nil {
		return answer(c)
	}
	return nil, nil
}

func (f *fakeExec) Start(_ context.Context, c Cmd) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStart != "" && strings.HasSuffix(c.Name, f.failStart) {
		return nil, errors.New("fake start failure")
	}
	p := &fakeProc{cmd: c, done: make(chan struct{})}
	f.started = append(f.started, p)
	if f.startCh != nil {
		f.startCh <- p
	}
	return p, nil
}

// lines returns every Run as one "name arg arg" line.
func (f *fakeExec) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.runs))
	for i, c := range f.runs {
		out[i] = strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " "))
	}
	return out
}

// startedNames returns the program of every background process, in order.
func (f *fakeExec) startedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.started))
	for i, p := range f.started {
		out[i] = p.cmd.Name
	}
	return out
}

// fakeProc is a background program the test ends.
type fakeProc struct {
	cmd  Cmd
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	// terminated and killed record how the group tried to stop it.
	terminated, killed bool
	exitErr            error
	// ignoreTerm makes the program survive Terminate, as a hung one would.
	ignoreTerm bool
}

func (p *fakeProc) Wait() error { <-p.done; return p.exitErr }

func (p *fakeProc) Terminate() error {
	p.mu.Lock()
	p.terminated = true
	ignore := p.ignoreTerm
	p.mu.Unlock()
	if !ignore {
		p.exit(nil)
	}
	return nil
}

func (p *fakeProc) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.exit(errors.New("killed"))
	return nil
}

// exit ends the program with err, once.
func (p *fakeProc) exit(err error) {
	p.once.Do(func() { p.exitErr = err; close(p.done) })
}
