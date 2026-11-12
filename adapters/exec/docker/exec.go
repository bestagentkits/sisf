package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bestagentkits/sisf/internal/secretbroker"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// Exec runs one command in a fresh container of the sandbox and removes
// the container when the command ends. The command's own timeout, if any,
// and the time the sandbox has left both bound it; past the bound the
// container is stopped and the result says it timed out. The run's secrets
// are redeemed for each command, so a revoked run gets none.
func (b *Backend) Exec(ctx context.Context, id execution.SandboxID, cmd execution.Command) (execution.ExecResult, error) {
	if len(cmd.Argv) == 0 {
		return execution.ExecResult{}, invalid("a command needs arguments")
	}
	if cmd.Stdin != nil {
		return execution.ExecResult{}, core.NewError(core.CodeNotSupported, "the docker backend cannot feed a command's standard input")
	}
	sb, name, timeout, err := b.reserve(id, cmd.Timeout)
	if err != nil {
		return execution.ExecResult{}, err
	}
	secretEnv, err := b.redeem(ctx, sb)
	if err != nil {
		return execution.ExecResult{}, err
	}
	req, err := b.containerRequest(sb, cmd, secretEnv)
	if err != nil {
		return execution.ExecResult{}, err
	}
	containerID, err := b.api.create(ctx, name, req)
	if err != nil {
		return execution.ExecResult{}, err
	}
	cleanup := context.WithoutCancel(ctx)
	defer func() {
		_ = b.api.remove(cleanup, containerID) // Destroy removes what this misses
		b.mu.Lock()
		delete(sb.running, containerID)
		b.mu.Unlock()
	}()
	view, err := b.api.inspect(ctx, containerID)
	if err != nil {
		return execution.ExecResult{}, err
	}
	if err := b.verifyApplied(view, req); err != nil {
		return execution.ExecResult{}, err
	}
	if !b.track(sb, containerID) {
		return execution.ExecResult{}, core.NewError(core.CodeNotFound, "the sandbox was destroyed")
	}
	started := b.now()
	if err := b.api.start(ctx, containerID); err != nil {
		return execution.ExecResult{}, err
	}
	code, timedOut, err := b.await(ctx, containerID, timeout)
	if err != nil {
		return execution.ExecResult{}, err
	}
	result := execution.ExecResult{ExitCode: code, TimedOut: timedOut, Duration: b.now().Sub(started)}
	if err := b.collect(cleanup, containerID, cmd, &result); err != nil {
		return result, err
	}
	return result, nil
}

// reserve checks the sandbox can run another command and returns the name
// of its container and the time it may take.
func (b *Backend) reserve(id execution.SandboxID, cmdTimeout time.Duration) (*sandbox, string, time.Duration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sb, ok := b.sandboxes[id]
	switch {
	case !ok:
		return nil, "", 0, core.NewError(core.CodeNotFound, "no such sandbox")
	case sb.info.State != execution.SandboxRunning:
		return nil, "", 0, core.NewError(core.CodeNotFound, "the sandbox was destroyed")
	}
	left := sb.deadline.Sub(b.now())
	if left <= 0 {
		return nil, "", 0, core.NewError(core.CodeLimitExceeded, "the sandbox has used up its time")
	}
	timeout := left
	if cmdTimeout > 0 && cmdTimeout < left {
		timeout = cmdTimeout
	}
	sb.execs++
	return sb, fmt.Sprintf("%s%s-%s-%d", b.cfg.NamePrefix, sb.spec.RunID, string(sb.info.ID)[len("sbx-"):], sb.execs), timeout, nil
}

// track records a container as running in the sandbox, unless the sandbox
// was destroyed meanwhile.
func (b *Backend) track(sb *sandbox, containerID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sb.info.State != execution.SandboxRunning {
		return false
	}
	sb.running[containerID] = true
	return true
}

// redeem fetches the run's secrets for one command.
func (b *Backend) redeem(ctx context.Context, sb *sandbox) (map[string]string, error) {
	if len(sb.grants) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(sb.grants))
	for grantID, name := range sb.grants {
		value, err := b.cfg.Secrets.RedeemFor(ctx, secretbroker.Use{GrantID: grantID, RunID: sb.spec.RunID,
			Purpose: secretbroker.PurposeSandboxEnv, Scope: secretbroker.EnvScope(name)})
		if err != nil {
			return nil, err
		}
		env[name] = string(value.Reveal())
	}
	return env, nil
}

// await waits for the container to exit, stopping it once timeout has
// passed on the backend's clock or the caller gave up.
func (b *Backend) await(ctx context.Context, containerID string, timeout time.Duration) (int, bool, error) {
	type exit struct {
		code int
		err  error
	}
	waitCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	done := make(chan exit, 1)
	go func() {
		code, err := b.api.wait(waitCtx, containerID)
		done <- exit{code, err}
	}()
	select {
	case e := <-done:
		return e.code, false, e.err
	case <-b.cfg.Clock.After(timeout):
		if err := b.api.stop(waitCtx, containerID); err != nil {
			return 0, true, err
		}
		e := <-done
		return e.code, true, e.err
	case <-ctx.Done():
		_ = b.api.stop(waitCtx, containerID) // the deferred remove forces it if this fails
		<-done
		return 0, false, ctx.Err()
	}
}

// collect delivers the container's output to the command's writers, or
// into the result, capped.
func (b *Backend) collect(ctx context.Context, containerID string, cmd execution.Command, result *execution.ExecResult) error {
	var outBuf, errBuf bytes.Buffer
	stdout, stderr := cmd.Stdout, cmd.Stderr
	if stdout == nil {
		stdout = &capped{w: &outBuf, left: b.cfg.MaxOutputBytes}
	}
	if stderr == nil {
		stderr = &capped{w: &errBuf, left: b.cfg.MaxOutputBytes}
	}
	if err := b.api.logs(ctx, containerID, stdout, stderr); err != nil {
		return err
	}
	if cmd.Stdout == nil {
		result.Stdout = outBuf.Bytes()
	}
	if cmd.Stderr == nil {
		result.Stderr = errBuf.Bytes()
	}
	return nil
}

// capped keeps the first left bytes written and drops the rest.
type capped struct {
	w    io.Writer
	left int64
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	c.left -= int64(len(p))
	if _, err := c.w.Write(p); err != nil {
		return 0, err
	}
	return n, nil
}
