package detect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/bestagentkits/sisf/pkg/managedapp"
)

// goProgram knows a Go service: a directory holding a main package. Its
// bundle is the one static program the build sandbox compiles from the
// candidate, run in a small pinned image as an unprivileged user.
type goProgram struct{}

func (goProgram) Name() string { return "go-program" }

func (goProgram) Match(app *App) bool {
	if app.compose != nil {
		return false
	}
	files, _ := filepath.Glob(filepath.Join(app.Dir, "*.go"))
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err == nil && (bytes.Contains(data, []byte("\npackage main\n")) || bytes.HasPrefix(data, []byte("package main\n"))) {
			return true
		}
	}
	return false
}

// goRuntimeImage is the image a Go program runs in: the Alpine release the
// factory's own images use, pinned by its multi-platform index.
const goRuntimeImage = "alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f"

func (goProgram) Describe(app *App, s *managedapp.Spec) error {
	name := filepath.Base(filepath.Clean(app.Dir))
	s.Runtime = managedapp.Runtime{
		Image: goRuntimeImage, Port: app.opts.Port, User: "10001:10001", HealthPath: "/healthz",
		Command: []string{"/app/" + name}, Env: nilIfEmpty(app.opts.Env),
	}
	if s.Runtime.Port == 0 {
		s.Runtime.Port = 8080
		app.Note("runtime.port: none given; 8080 is assumed. Pass --port with the port the program listens on")
	}
	s.Bundle = managedapp.Bundle{Program: "./" + filepath.ToSlash(app.relDir()), Mount: "/app"}
	for _, d := range app.opts.Data {
		s.Data = append(s.Data, managedapp.Volume{Name: d.Name, Target: d.Target, Owner: s.Runtime.User})
	}
	s.Checks = []managedapp.Check{
		{Name: "health", MaxLatencyMS: 1000, Steps: []managedapp.Step{{Path: "/healthz"}}},
		{Name: "home", MaxLatencyMS: 2000, Steps: []managedapp.Step{{Path: "/"}}},
	}
	s.Policy = managedapp.Policy{Protected: []string{"go.mod", "go.sum"}}
	app.Note("checks: only /healthz and / are checked; add the journeys your users depend on")
	return nil
}
