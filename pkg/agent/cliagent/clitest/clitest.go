//go:build unix

// Package clitest gives tests a stand-in for an agent CLI. Install puts an
// executable of the given name in a temporary directory; when it is run it
// follows a Script: it prints recorded output, reads input, writes files,
// hangs or exits. The stand-in is a small program built once per test
// process from this module, so no real CLI, token or network is involved.
package clitest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	envSpec  = "SISF_CLITEST_SPEC"
	envChild = "SISF_CLITEST_CHILD"
)

// Script is what the stand-in does when it is run.
type Script struct {
	// Version is printed for "--version". "1.2.3 (stub)" when empty.
	Version string `json:"version,omitempty"`
	// Record is a directory where the run leaves args.json, stdin.txt,
	// cwd.txt and env.txt. Nothing is recorded when empty.
	Record string `json:"record,omitempty"`
	Steps  []Step `json:"steps"`
}

// Step is one action. Set exactly one field.
type Step struct {
	// Emit prints a line to stdout.
	Emit string `json:"emit,omitempty"`
	// EmitFile prints every line of a recorded fixture to stdout.
	EmitFile string `json:"emit_file,omitempty"`
	// Stderr prints a line to stderr.
	Stderr string `json:"stderr,omitempty"`
	// AwaitStdin reads stdin until a line contains the text; until end of
	// input when the text is "*EOF*".
	AwaitStdin string `json:"await_stdin,omitempty"`
	// WriteFile creates files relative to the working directory.
	WriteFile map[string]string `json:"write_file,omitempty"`
	// EchoEnv prints the value of an environment variable to stdout.
	EchoEnv string `json:"echo_env,omitempty"`
	// SpawnChild starts a child process that never exits on its own and
	// writes its pid to the file.
	SpawnChild string `json:"spawn_child,omitempty"`
	// Hang blocks until the process is killed.
	Hang bool `json:"hang,omitempty"`
	// Exit ends the process with the status.
	Exit *int `json:"exit,omitempty"`
}

// Exit returns a pointer for Step.Exit.
func Exit(code int) *int { return &code }

// Main is the stand-in's entry point, called by the program in ./stub. It
// returns the process exit status.
func Main() int {
	if os.Getenv(envChild) != "" {
		block()
	}
	return runStub(os.Getenv(envSpec))
}

var (
	buildOnce sync.Once
	stubPath  string
	buildErr  error
)

// stub builds the stand-in program once and returns its path.
func stub() (string, error) {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sisf-clitest-")
		if err != nil {
			buildErr = err
			return
		}
		stubPath = filepath.Join(dir, "stub")
		out, err := exec.Command("go", "build", "-o", stubPath, "github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest/stub").CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("build the stand-in: %v: %s", err, out)
		}
	})
	return stubPath, buildErr
}

// Install writes an executable called name into a new directory and returns
// the directory, to be put on a PATH.
func Install(t testing.TB, name string, s Script) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("encode script: %v", err)
	}
	specPath := filepath.Join(dir, name+".spec.json")
	if err := os.WriteFile(specPath, raw, 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	self, err := stub()
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("#!/bin/sh\n%s='%s' exec '%s' \"$@\"\n", envSpec, specPath, self)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
		t.Fatalf("write stand-in: %v", err)
	}
	return dir
}

// Env returns a minimal environment whose PATH is bin and the system
// directories, plus extra entries.
func Env(bin string, extra ...string) []string {
	return append([]string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + os.TempDir()}, extra...)
}

// Recording is what a recorded run saw.
type Recording struct {
	Args  []string
	Stdin string
	Cwd   string
	Env   []string
	// Pid is the process id of the stand-in, to tell runs apart.
	Pid int
}

// Recorded reads what the stand-in recorded into dir.
func Recorded(t testing.TB, dir string) Recording {
	t.Helper()
	var rec Recording
	if raw, err := os.ReadFile(filepath.Join(dir, "args.json")); err == nil {
		if err := json.Unmarshal(raw, &rec.Args); err != nil {
			t.Fatalf("decode recorded args: %v", err)
		}
	} else {
		t.Fatalf("read recorded args: %v", err)
	}
	read := func(name string) string {
		raw, _ := os.ReadFile(filepath.Join(dir, name))
		return string(raw)
	}
	rec.Stdin, rec.Cwd = read("stdin.txt"), strings.TrimSpace(read("cwd.txt"))
	rec.Env = strings.Split(strings.TrimSpace(read("env.txt")), "\n")
	rec.Pid, _ = strconv.Atoi(strings.TrimSpace(read("pid.txt")))
	return rec
}

func runStub(specPath string) int {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stand-in: cannot read its script")
		return 97
	}
	var s Script
	if err := json.Unmarshal(raw, &s); err != nil {
		fmt.Fprintln(os.Stderr, "stand-in: bad script")
		return 97
	}
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		v := s.Version
		if v == "" {
			v = "1.2.3 (stub)"
		}
		fmt.Println(v)
		return 0
	}
	if s.Record != "" {
		record(s.Record, args)
	}
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	for _, step := range s.Steps {
		if code, stop := perform(s, step, in); stop {
			return code
		}
	}
	return 0
}

func record(dir string, args []string) {
	_ = os.MkdirAll(dir, 0o700)
	rawArgs, _ := json.Marshal(args)
	_ = os.WriteFile(filepath.Join(dir, "args.json"), rawArgs, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pid.txt"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	cwd, _ := os.Getwd()
	_ = os.WriteFile(filepath.Join(dir, "cwd.txt"), []byte(cwd), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "env.txt"), []byte(strings.Join(os.Environ(), "\n")), 0o600)
}

func perform(s Script, step Step, in *bufio.Reader) (code int, stop bool) {
	switch {
	case step.Emit != "":
		fmt.Println(step.Emit)
	case step.EmitFile != "":
		data, err := os.ReadFile(step.EmitFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "stand-in: cannot read fixture")
			return 98, true
		}
		_, _ = os.Stdout.Write(data)
	case step.Stderr != "":
		fmt.Fprintln(os.Stderr, step.Stderr)
	case step.AwaitStdin != "":
		await(s, step.AwaitStdin, in)
	case step.EchoEnv != "":
		fmt.Println(os.Getenv(step.EchoEnv))
	case step.WriteFile != nil:
		for name, content := range step.WriteFile {
			if err := os.MkdirAll(filepath.Dir(name), 0o755); err == nil {
				_ = os.WriteFile(name, []byte(content), 0o644)
			}
		}
	case step.SpawnChild != "":
		self, _ := os.Executable()
		child := exec.Command(self)
		child.Env = append(os.Environ(), envChild+"=1")
		if err := child.Start(); err == nil {
			// Rename makes the pid file appear complete or not at all.
			if os.WriteFile(step.SpawnChild+".tmp", []byte(strconv.Itoa(child.Process.Pid)), 0o600) == nil {
				_ = os.Rename(step.SpawnChild+".tmp", step.SpawnChild)
			}
		}
	case step.Hang:
		block()
	case step.Exit != nil:
		return *step.Exit, true
	}
	return 0, false
}

// await reads stdin until a line contains text, appending what it reads to
// the recorded stdin.
func await(s Script, text string, in *bufio.Reader) {
	var out *os.File
	if s.Record != "" {
		out, _ = os.OpenFile(filepath.Join(s.Record, "stdin.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		defer func() {
			if out != nil {
				_ = out.Close()
			}
		}()
	}
	for {
		line, err := in.ReadString('\n')
		if out != nil && line != "" {
			_, _ = out.WriteString(line)
		}
		if err != nil || (text != "*EOF*" && strings.Contains(line, text)) {
			return
		}
	}
}

// block waits until the process is killed. A bare select would be reported
// by the runtime as a deadlock and end the process.
func block() {
	for {
		time.Sleep(time.Hour)
	}
}
