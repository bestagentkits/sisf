package install

import (
	"context"
	"io"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Installer acts on one install directory. Everything that touches the
// outside, such as running programs, the clock and the network, is a field,
// so a test can replace it and the default test run starts nothing.
type Installer struct {
	Layout Layout
	// Exec runs the programs `up` needs: go build, docker, the programs of
	// the Standalone process group.
	Exec Exec
	// Clock stamps the confirmation and the release, and paces restarts.
	Clock core.Clock
	// Out receives progress for a person. It never receives a secret.
	Out io.Writer
	// Rand is the source of every generated secret; nil means crypto/rand.
	Rand io.Reader
	// HTTP is the client of the doctor's probes.
	HTTP *http.Client
	// SourceDir is the repository the programs and images are built from.
	SourceDir string
	// Sisf is the path of the running sisf program, written into the
	// service units.
	Sisf string
	// Debug publishes the state store on 127.0.0.1:7469 in Docker mode.
	Debug bool
	// LookPath finds a program on PATH; nil means exec.LookPath.
	LookPath func(name string) (string, error)
	// Signal sends a signal to a process, or to a process group for a
	// negative pid; nil means syscall.Kill.
	Signal func(pid int, sig syscall.Signal) error
	// ProcessStart says when a process started, as ps prints it in the C
	// locale, or "" when no such process lives; nil means ps. It tells a
	// recorded process from another that was later given its pid.
	ProcessStart func(pid int) (string, error)
	// SupervisorStatus asks the supervisor on a loopback address for its
	// status; nil means supervisor.FetchStatus.
	SupervisorStatus func(ctx context.Context, addr string) (supervisor.Status, error)

	outMu sync.Mutex // units log from their own goroutines
}

// New returns an installer for dir that runs real programs and uses the wall
// clock.
func New(dir string, out io.Writer) *Installer {
	exe, _ := os.Executable() // an unknown path only affects the generated service units
	return &Installer{
		Layout: Layout{Dir: dir}, Exec: OSExec{}, Clock: core.SystemClock{}, Out: out,
		HTTP: &http.Client{Timeout: 5 * time.Second}, Sisf: exe,
	}
}

// say writes progress. A closed output stream is not a reason to stop.
func (in *Installer) say(format string, args ...any) {
	in.outMu.Lock()
	defer in.outMu.Unlock()
	if in.Out != nil {
		_, _ = io.WriteString(in.Out, sprintf(format, args...))
	}
}
