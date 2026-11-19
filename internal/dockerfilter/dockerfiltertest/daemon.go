// Package dockerfiltertest is an in-memory stand-in for the Docker daemon,
// for tests of the Docker request filter and of the docker execution
// adapter. It speaks the few endpoints a runner's and a revision's life
// needs, named volumes and archive uploads among them, records every
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
	// Archives are the tar streams uploaded into the container, in order.
	Archives []Archive
	done     chan struct{}
}

// Archive is one upload into a container: the path it was extracted at and
// the bytes of the tar stream.
type Archive struct {
	Path string
	Body []byte
}

// Volume is the daemon's record of one named volume.
type Volume struct {
	Name   string
	Labels map[string]string
}

// Daemon is the fake daemon. It is safe for concurrent use.
type Daemon struct {
	mu         sync.Mutex
	containers map[string]*Container
	order      []string
	requests   []Request
	seq        int
	runtimes   []string
	networks   map[string]bool
	volumes    map[string]*Volume
	// failArchive makes every archive upload answer 500 when set.
	failArchive bool
	// Run decides what a started container does; it exits 0 at once when nil.
	run func(Container) Outcome
}

// New returns a daemon that reports the given runtimes.
func New(runtimes ...string) *Daemon {
	return &Daemon{containers: map[string]*Container{}, runtimes: runtimes, networks: map[string]bool{}, volumes: map[string]*Volume{}}
}

// AddVolume records a named volume that was not made through the API.
func (d *Daemon) AddVolume(name string, labels map[string]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.volumes[name] = &Volume{Name: name, Labels: labels}
}

// Volumes returns a copy of every volume that exists, by name.
func (d *Daemon) Volumes() map[string]Volume {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]Volume, len(d.volumes))
	for name, v := range d.volumes {
		out[name] = *v
	}
	return out
}

// FailArchives makes every archive upload from now on fail, or succeed again.
func (d *Daemon) FailArchives(fail bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failArchive = fail
}

// AddNetwork records a user-defined network, internal or not. A network
// that was not added is unknown to the daemon.
func (d *Daemon) AddNetwork(name string, internal bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.networks[name] = internal
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
	case path == "/containers/json" && r.Method == http.MethodGet:
		d.list(w, r)
	case len(parts) == 2 && parts[0] == "networks" && r.Method == http.MethodGet:
		d.network(w, parts[1])
	case path == "/volumes/create" && r.Method == http.MethodPost:
		d.createVolume(w, body)
	case len(parts) == 2 && parts[0] == "volumes":
		d.volume(w, r, parts[1])
	case len(parts) >= 2 && parts[0] == "containers":
		d.container(w, r, parts[1], strings.Join(parts[2:], "/"), body)
	default:
		// A real daemon would have done this; answering keeps the test
		// honest about what got through.
		writeJSON(w, http.StatusOK, map[string]string{"message": "done"})
	}
}

// createVolume makes a named volume, or answers with the one of that name
// that exists, as the daemon does.
func (d *Daemon) createVolume(w http.ResponseWriter, body []byte) {
	var cfg struct {
		Name   string
		Labels map[string]string
	}
	if err := json.Unmarshal(body, &cfg); err != nil || cfg.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad body"})
		return
	}
	d.mu.Lock()
	v, ok := d.volumes[cfg.Name]
	if !ok {
		v = &Volume{Name: cfg.Name, Labels: cfg.Labels}
		d.volumes[cfg.Name] = v
	}
	view := volumeView(v)
	d.mu.Unlock()
	writeJSON(w, http.StatusCreated, view)
}

// volume inspects or removes a named volume. A volume a container still
// mounts is not removed.
func (d *Daemon) volume(w http.ResponseWriter, r *http.Request, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.volumes[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "get " + name + ": no such volume"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, volumeView(v))
	case http.MethodDelete:
		for _, c := range d.containers {
			if mountsVolume(c, name) {
				writeJSON(w, http.StatusConflict, map[string]string{"message": "remove " + name + ": volume is in use"})
				return
			}
		}
		delete(d.volumes, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"message": "done"})
	}
}

func volumeView(v *Volume) map[string]any {
	return map[string]any{"Name": v.Name, "Labels": v.Labels, "Driver": "local",
		"Mountpoint": "/var/lib/docker/volumes/" + v.Name + "/_data", "Scope": "local"}
}

// mountsVolume reports whether the container's mounts name the volume.
func mountsVolume(c *Container, name string) bool {
	mounts, _ := c.Host["Mounts"].([]any)
	for _, m := range mounts {
		if m, ok := m.(map[string]any); ok && m["Source"] == name {
			return true
		}
	}
	return false
}

// network answers an inspect of a network by name.
func (d *Daemon) network(w http.ResponseWriter, name string) {
	d.mu.Lock()
	internal, ok := d.networks[name]
	d.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "network " + name + " not found"})
		return
	}
	sum := sha256.Sum256([]byte("network-" + name))
	writeJSON(w, http.StatusOK, map[string]any{"Name": name, "Id": hex.EncodeToString(sum[:]), "Driver": "bridge",
		"Internal": internal, "Containers": map[string]any{}})
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

// list answers a container list, applying its label filters.
func (d *Daemon) list(w http.ResponseWriter, r *http.Request) {
	var filters map[string][]string
	if raw := r.URL.Query().Get("filters"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &filters); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "bad filters"})
			return
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []map[string]any{}
	for _, id := range d.order {
		c, ok := d.containers[id]
		if !ok || !hasLabels(c.Labels, filters["label"]) {
			continue
		}
		state := "created"
		if c.Running {
			state = "running"
		}
		out = append(out, map[string]any{"Id": c.ID, "Names": []string{"/" + c.Name}, "Labels": c.Labels, "State": state})
	}
	writeJSON(w, http.StatusOK, out)
}

// hasLabels reports whether labels carry every key=value of want.
func hasLabels(labels map[string]string, want []string) bool {
	for _, kv := range want {
		k, v, _ := strings.Cut(kv, "=")
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
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

func (d *Daemon) container(w http.ResponseWriter, r *http.Request, ref, action string, body []byte) {
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
	case action == "archive" && r.Method == http.MethodPut:
		fail := d.failArchive
		if !fail {
			c.Archives = append(c.Archives, Archive{Path: r.URL.Query().Get("path"), Body: body})
		}
		d.mu.Unlock()
		if fail {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "extraction failed"})
			return
		}
		w.WriteHeader(http.StatusOK)
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
		"Id": c.ID, "Name": "/" + c.Name, "Image": imageID(c.Image),
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

// imageID is the ID of the image a container was created from, as the
// daemon reports it: the reference itself when it is an image ID, and a
// fixed stand-in for the local image a registry digest resolves to.
func imageID(ref string) string {
	if rest, ok := strings.CutPrefix(ref, "sha256:"); ok && len(rest) == 64 {
		return ref
	}
	return "sha256:" + strings.Repeat("0", 64)
}
