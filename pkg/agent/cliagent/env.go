package cliagent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/agent/redact"
	"github.com/bestagentkits/sisf/pkg/core"
)

// versionTimeout bounds the "--version" probe. It is a guard on a helper
// process, not a run limit, so it uses real time.
const versionTimeout = 15 * time.Second

// maxVersionLen bounds the version string kept in a run record.
const maxVersionLen = 128

// envValue returns the last value of name in env, and whether it is set.
func envValue(env []string, name string) (string, bool) {
	prefix := name + "="
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], prefix); ok {
			return v, true
		}
	}
	return "", false
}

// cleanEnv drops entries that are not KEY=VALUE.
func cleanEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && name != "" {
			out = append(out, kv)
		}
	}
	return out
}

// findBinary resolves name against the PATH of env. An entry that is not an
// absolute directory is skipped, so a binary planted in the working
// directory is never picked up. A name with a path separator is used as is.
func findBinary(name string, env []string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		return exec.LookPath(name)
	}
	path, ok := envValue(env, "PATH")
	if !ok {
		path = os.Getenv("PATH")
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", core.NewError(core.CodeUnavailable, "the "+name+" program was not found on PATH")
}

// probeVersion runs "<binary> --version" and returns its first output line,
// redacted and bounded.
func probeVersion(ctx context.Context, bin string, env []string, redactor *redact.Redactor) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = env
	cmd.Dir = os.TempDir()
	isolate(cmd)
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, left: 4096}
	if err := cmd.Run(); err != nil {
		return "", core.NewError(core.CodeUnavailable, "the version of the "+filepath.Base(bin)+" program could not be read")
	}
	for line := range strings.SplitSeq(out.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return Clip(redactor.Text(line), maxVersionLen), nil
		}
	}
	return "", core.NewError(core.CodeUnavailable, "the "+filepath.Base(bin)+" program reported no version")
}

// limitedWriter keeps at most left bytes and silently discards the rest.
type limitedWriter struct {
	w    *bytes.Buffer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if l.left > 0 {
		if len(p) > l.left {
			p = p[:l.left]
		}
		l.w.Write(p)
		l.left -= len(p)
	}
	return n, nil
}

// Clip shortens s to at most n bytes without cutting a UTF-8 sequence, and
// marks the cut.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
