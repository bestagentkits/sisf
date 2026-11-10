//go:build smoke

package smoke

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bestagentkits/sisf/internal/install"
)

const (
	// upLimit bounds how long `sisf up` may take to build and start every unit.
	upLimit = 10 * time.Minute
	// downLimit bounds `sisf down`, which waits out every unit's grace period.
	downLimit = 2 * time.Minute
	// pollInterval is the pause between two looks at a waiting condition.
	pollInterval = 2 * time.Second
)

func loopbackURL(port int, path string) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + path
}

// factoryProcess is the foreground `sisf up` the run started.
type factoryProcess struct {
	cmd    *exec.Cmd
	exited chan struct{}
	log    string
}

// startFactory runs `sisf up` in the background, in a process group of its
// own, so that an interrupt of the test reaches the test, which then stops
// the factory through `sisf down`.
func startFactory(r *commandRunner, bin string, args, env []string, log string) (*factoryProcess, error) {
	f, err := os.Create(log)
	if err != nil {
		return nil, err
	}
	r.report.Commands = append(r.report.Commands, r.scrub.clean(bin+" "+strings.Join(args, " ")))
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = r.dir, env, f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return nil, err
	}
	p := &factoryProcess{cmd: cmd, exited: make(chan struct{}), log: log}
	go func() {
		_ = cmd.Wait() // the exit is observed through exited
		_ = f.Close()
		close(p.exited)
	}()
	return p, nil
}

func (p *factoryProcess) running() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// waitUntilUp polls the units until each answers, failing at once when
// `sisf up` gives up first.
func (p *factoryProcess) waitUntilUp(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, upLimit)
	defer cancel()
	checks := []func() error{
		func() error { return httpOK(ctx, loopbackURL(install.PortControl, "/healthz")) },
		func() error { return httpOK(ctx, loopbackURL(install.PortDispatcher, "/healthz")) },
		func() error { return httpOK(ctx, loopbackURL(install.PortEdge, "/healthz")) },
		func() error { return dial("127.0.0.1:" + strconv.Itoa(install.PortSupervisor)) },
	}
	for {
		if !p.running() {
			return fmt.Errorf("sisf up exited before the factory was up:\n%s", tailFile(p.log, 40))
		}
		var last error
		for _, check := range checks {
			if last = check(); last != nil {
				break
			}
		}
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the factory was not up within %s; last seen: %v\n%s", upLimit, last, tailFile(p.log, 40))
		case <-time.After(pollInterval):
		}
	}
}

// stop runs `sisf down` for the install, waits for `sisf up` to end and,
// only if it does not, stops the process group it started itself. It
// returns what happened, for the report.
func (p *factoryProcess) stop(r *commandRunner, bin, dir string, env []string) string {
	var notes []string
	if _, err := r.run(context.Background(), downLimit, env, bin, downArgs(dir)...); err != nil {
		notes = append(notes, fmt.Sprintf("sisf down failed: %v", err))
	} else {
		notes = append(notes, "sisf down --dir <install> succeeded")
	}
	select {
	case <-p.exited:
		notes = append(notes, "sisf up exited")
	case <-time.After(30 * time.Second):
		notes = append(notes, "sisf up was still running 30s after sisf down; its process group was sent SIGTERM")
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-p.exited:
		case <-time.After(15 * time.Second):
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			notes = append(notes, "and then SIGKILL")
		}
	}
	if busy := busyPorts(); len(busy) > 0 {
		notes = append(notes, fmt.Sprintf("ports still busy after teardown: %v", busy))
	} else {
		notes = append(notes, "ports 7460-7469 free after teardown")
	}
	return strings.Join(notes, "; ")
}

func httpOK(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	return nil
}

func dial(address string) error {
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

// tailFile returns the last n lines of a file, or why it cannot be read.
func tailFile(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "(" + filepath.Base(path) + " cannot be read: " + err.Error() + ")"
	}
	return lastLines(string(raw), n)
}

// logTails collects the end of `sisf up`'s output and of every unit log,
// as evidence of a failure. The caller redacts it.
func logTails(upLog, home string) []string {
	out := []string{"sisf up:\n" + tailFile(upLog, 40)}
	logs, _ := filepath.Glob(filepath.Join(install.Layout{Dir: home}.Logs(), "*.log"))
	for _, l := range logs {
		out = append(out, filepath.Base(l)+":\n"+tailFile(l, 40))
	}
	return out
}
