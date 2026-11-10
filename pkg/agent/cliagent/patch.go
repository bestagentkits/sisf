package cliagent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// emptyTree is the id of git's empty tree. Used as the attribute source it
// makes git ignore every attributes file in the workspace.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// gitTimeout bounds each git command run on a workspace.
const gitTimeout = time.Minute

// dangerousConfig matches repository settings that make git run a command
// the agent could have written: filters, fsmonitor, hooks, external diff and
// the like. A workspace that sets one is refused, not patched.
var dangerousConfig = regexp.MustCompile(`(?i)^(filter\..+|diff\..+\.(command|textconv)|merge\..+\.driver|core\.(fsmonitor|hookspath|sshcommand|pager|editor|askpass|gitproxy|attributesfile|excludesfile)|include\..*|includeif\..*|alias\..+|credential\..+|url\..+|remote\..+\.(vcs|uploadpack|receivepack)|uploadpack\..+|gpg\..+|sequence\.editor)$`)

// collectPatch returns the changes the agent left in a git workspace,
// including new files, relative to the base commit. It uses a throwaway index
// so the workspace's own index and files are untouched. A workspace that is
// not a git repository has no patch (nil, nil); one whose configuration would
// make git execute a command is refused.
func collectPatch(ctx context.Context, ws agent.WorkspaceRef, scratch string) ([]byte, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	if out, err := git(ctx, ws.Path, nil, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil, nil
	}
	names, err := git(ctx, ws.Path, nil, "config", "--local", "--list", "--name-only")
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the workspace git configuration could not be read")
	}
	for name := range strings.SplitSeq(string(names), "\n") {
		if dangerousConfig.MatchString(strings.TrimSpace(name)) {
			return nil, core.NewError(core.CodePermissionDenied, "the workspace git configuration defines commands git would run, so no patch was collected")
		}
	}
	index, err := os.CreateTemp(scratch, "index-*")
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "a scratch index could not be created")
	}
	_ = index.Close()
	defer func() { _ = os.Remove(index.Name()) }()
	// git read-tree needs the index not to exist as an empty file.
	_ = os.Remove(index.Name())
	env := []string{"GIT_INDEX_FILE=" + index.Name()}

	base := string(ws.BaseSHA)
	if base == "" {
		base = "HEAD"
	}
	if _, err := git(ctx, ws.Path, env, "read-tree", base); err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the base commit of the workspace could not be read")
	}
	if _, err := git(ctx, ws.Path, env, "add", "-A"); err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the workspace changes could not be staged for diffing")
	}
	patch, err := git(ctx, ws.Path, env, "diff", "--cached", "--binary", "--no-color", "--no-ext-diff", "--no-textconv", base)
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable, "the workspace patch could not be computed")
	}
	return patch, nil
}

// git runs one git command in dir with a clean environment: no user or
// system configuration, no attributes, no hooks, no fsmonitor.
func git(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	full := append([]string{
		"-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.pager=cat", "-c", "core.quotepath=off",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = filepath.Clean(dir)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.DevNull,
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_SOURCE=" + emptyTree, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0",
	}, env...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}
