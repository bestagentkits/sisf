// Package dockerfiltertest is an in-memory stand-in for the Docker daemon,
// for tests of the Docker request filter and of the docker execution
// adapter. It speaks the few endpoints a runner's life needs, records every
// request that reaches it and answers 200 to anything else, so a test can
// prove that a refused request never reached the daemon. It runs nothing.
package dockerfiltertest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// Request is one request that reached the daemon.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   []byte
}

// Outcome is what a started container does.
type Outcome struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// Hang keeps the container running until it is stopped.
	Hang bool
}

// Container is the daemon's record of one container.
type Container struct {
	ID       string
	Name     string
	Labels   map[string]string
	Image    string
	Env      []string
	Cmd      []string
	User     string
	Host     map[string]any
	Started  bool
	Running  bool
	ExitCode int
	Stdout   string
	Stderr   string
	done     chan struct{}
}

// Daemon is the fake daemon. It is safe for concurrent use.
type Daemon struct {
	mu         sync.Mutex
	containers map[string]*Container
	order      []string
	requests   []Request
	seq        int
	runtimes   []string
	// Run decides what a started container does; it exits 0 at once when nil.
	run func(Container) Outcome
}

// New returns a daemon that reports the given runtimes.
func New(runtimes ...string) *Daemon {
	return &Daemon{containers: map[string]*Container{}, runtimes: runtimes}
}

// OnStart sets what every container started from now on does.
func (d *Daemon) OnStart(run func(Container) Outcome) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.run = run
}

// AddContainer records a running container that was not made through the
// API, such as the factory's control container, and returns its ID.
func (d *Daemon) AddContainer(name string, labels map[string]string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.newContainer(name)
	c.Labels, c.Started, c.Running = labels, true, true
	return c.ID
}

func (d *Daemon) newContainer(name string) *Container {
	d.seq++
	sum := sha256.Sum256([]byte(fmt.Sprintf("container-%d-%s", d.seq, name)))
	c := &Container{ID: hex.EncodeToString(sum[:]), Name: name, done: make(chan struct{})}
	d.containers[c.ID] = c
	d.order = append(d.order, c.ID)
	return c
}

// Requests returns every request that reached the daemon, in order.
func (d *Daemon) Requests() []Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Request(nil), d.requests...)
}

// Containers returns a copy of every container that still exists.
func (d *Daemon) Containers() []Container {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Container
	for _, id := range d.order {
		if c, ok := d.containers[id]; ok {
			out = append(out, *c)
		}
	}
	return out
}

// Created returns every container ever created through the API, removed or
// not, in order of creation.
func (d *Daemon) Created() []Request {
	var out []Request
	for _, r := range d.Requests() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/containers/create") {
			out = append(out, r)
		}
	}
	return out
}

var versioned = regexp.MustCompile(`^/v1\.[0-9]+(/.*)$`)

// ServeHTTP answers one request.
func (d *Daemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20)) // a short read only shortens the record
	d.mu.Lock()
	d.requests = append(d.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
	d.mu.Unlock()
	path := r.URL.Path
	if m := versioned.FindStringSubmatch(path); m != nil {
		path = m[1]
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/_ping":
		_, _ = io.WriteString(w, "OK")
	case path == "/info":
		d.info(w)
	case path == "/containers/create" && r.Method == http.MethodPost:
		d.create(w, r, body)
	case len(parts) >= 2 && parts[0] == "containers":
		d.container(w, r, parts[1], strings.Join(parts[2:], "/"))
	default:
		// A real daemon would have done this; answering keeps the test
		// honest about what got through.
		writeJSON(w, http.StatusOK, map[string]string{"message": "done"})
	}
}

func (d *Daemon) info(w http.ResponseWriter) {
	runtimes := map[string]any{}
	for _, name := range d.runtimes {
		runtimes[name] = map[string]string{"path": "/usr/bin/" + name}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Runtimes": runtimes, "DefaultRuntime": "runc", "Name": "build-host", "KernelVersion": "6.1.0",
		"SecurityOptions": []string{"name=seccomp,profile=builtin"}, "DockerRootDir": "/var/lib/docker",
	})
}

func (d *Daemon) create(w http.ResponseWriter, r *http.Request, body []byte) {
	var cfg struct {
		Image      string
		Cmd        []string
		Env        []string
		User       string
		Labels     map[string]string
		HostConfig map[string]any
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad body"})
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.newContainer(r.URL.Query().Get("name"))
	c.Image, c.Cmd, c.Env, c.User, c.Labels, c.Host = cfg.Image, cfg.Cmd, cfg.Env, cfg.User, cfg.Labels, cfg.HostConfig
	writeJSON(w, http.StatusCreated, map[string]any{"Id": c.ID, "Warnings": []string{}})
}

// find resolves a full ID, an ID prefix or a name. The caller holds d.mu.
func (d *Daemon) find(ref string) *Container {
	if c, ok := d.containers[ref]; ok {
		return c
	}
	for _, c := range d.containers {
		if c.Name == ref || (len(ref) >= 12 && strings.HasPrefix(c.ID, ref)) {
			return c
		}
	}
	return nil
}

func (d *Daemon) container(w http.ResponseWriter, r *http.Request, ref, action string) {
	d.mu.Lock()
	c := d.find(ref)
	if c == nil {
		d.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + ref})
		return
	}
	switch {
	case action == "json":
		view := d.view(c)
		d.mu.Unlock()
		writeJSON(w, http.StatusOK, view)
	case action == "start":
		d.start(c)
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case action == "stop":
		if c.Running {
			c.Running, c.ExitCode = false, 137
			close(c.done)
		}
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case action == "wait":
		done := c.done
		d.mu.Unlock()
		select {
		case <-done:
		case <-r.Context().Done():
			return
		}
		d.mu.Lock()
		code := c.ExitCode
		d.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"StatusCode": code})
	case action == "logs":
		out, errOut := c.Stdout, c.Stderr
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		_, _ = w.Write(frame(1, out))
		_, _ = w.Write(frame(2, errOut))
	case action == "" && r.Method == http.MethodDelete:
		if c.Running && r.URL.Query().Get("force") != "1" && r.URL.Query().Get("force") != "true" {
			d.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"message": "container is running"})
			return
		}
		if c.Running {
			c.Running = false
			close(c.done)
		}
		delete(d.containers, c.ID)
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		d.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"message": "done"})
	}
}

// start runs the container's outcome. The caller holds d.mu.
func (d *Daemon) start(c *Container) {
	if c.Started {
		return
	}
	outcome := Outcome{}
	if d.run != nil {
		outcome = d.run(*c)
	}
	c.Started, c.Running, c.Stdout, c.Stderr = true, true, outcome.Stdout, outcome.Stderr
	if !outcome.Hang {
		c.Running, c.ExitCode = false, outcome.ExitCode
		close(c.done)
	}
}

func (d *Daemon) view(c *Container) map[string]any {
	status := "created"
	switch {
	case c.Running:
		status = "running"
	case c.Started:
		status = "exited"
	}
	return map[string]any{
		"Id": c.ID, "Name": "/" + c.Name, "Image": "sha256:" + strings.Repeat("0", 64),
		"Config":     map[string]any{"Labels": c.Labels, "Image": c.Image, "Env": c.Env, "User": c.User, "Cmd": c.Cmd},
		"HostConfig": c.Host,
		"State":      map[string]any{"Status": status, "Running": c.Running, "ExitCode": c.ExitCode},
	}
}

// frame encodes one frame of the daemon's multiplexed log stream.
func frame(stream byte, payload string) []byte {
	if payload == "" {
		return nil
	}
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // a broken test connection fails the test elsewhere
}
