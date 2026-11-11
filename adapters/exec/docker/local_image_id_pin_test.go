package docker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// A runner image built on the host has no registry digest; pinned by its
// image ID, which Docker resolves locally and never pulls, it runs.
func TestARunnerImagePinnedByItsLocalImageIDRuns(t *testing.T) {
	t.Parallel()
	e := newEnv(t, options{})
	s := spec("run-0001", execution.TrustUntrusted)
	s.Image = localImage
	sb := e.create(t, s)
	res, err := e.backend.Exec(context.Background(), sb.ID, execution.Command{Argv: []string{"true"}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("a command in a runner pinned by its image ID: %+v %v", res, err)
	}
	if bodies := e.createdBodies(t); len(bodies) != 1 || bodies[0].Image != localImage {
		t.Fatalf("the daemon was not asked for a runner from %s: %+v", localImage, bodies)
	}
}

// Only a full content pin is a pin: a tag, a short or malformed ID and an
// image ID under another algorithm are refused before anything is created.
func TestImagesNotPinnedByTheirFullDigestAreRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t, options{})
	for name, img := range map[string]string{
		"short image ID":       "sha256:" + strings.Repeat("d", 12),
		"bare short ID":        strings.Repeat("d", 12),
		"bare full ID":         strings.Repeat("d", 64),
		"upper case ID":        "sha256:" + strings.Repeat("D", 64),
		"other algorithm":      "sha512:" + strings.Repeat("d", 128),
		"tag and ID":           "runner:build@sha256:" + strings.Repeat("d", 64) + "x",
		"ID one digit short":   "sha256:" + strings.Repeat("d", 63),
		"local tag":            "acme-app-runner:build",
		"digest with no name":  "@sha256:" + strings.Repeat("d", 64),
		"ID with a whitespace": " sha256:" + strings.Repeat("d", 64),
	} {
		s := spec("run-0001", execution.TrustUntrusted)
		s.Image = img
		_, err := e.backend.CreateSandbox(context.Background(), s)
		wantCode(t, err, core.CodeInvalidArgument, name)
	}
	if n := len(e.daemon.Created()); n != 0 {
		t.Fatalf("%d containers were created from unpinned images", n)
	}
}

// A daemon that reports a container made from another image than the pin,
// as an image re-tagged or replaced under the same reference would show,
// never gets it started.
func TestAContainerFromAnotherImageThanThePinIsNeverStarted(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		pin     string
		rewrite func(map[string]any)
	}{
		"another image ID": {localImage, func(v map[string]any) { v["Image"] = "sha256:" + strings.Repeat("e", 64) }},
		"another reference": {image, func(v map[string]any) {
			v["Config"].(map[string]any)["Image"] = "ghcr.io/acme/sisf-runner:latest"
		}},
	} {
		e := newEnv(t, options{rewrite: c.rewrite})
		s := spec("run-0001", execution.TrustUntrusted)
		s.Image = c.pin
		sb := e.create(t, s)
		_, err := e.backend.Exec(context.Background(), sb.ID, execution.Command{Argv: []string{"true"}})
		wantCode(t, err, core.CodeInternal, name)
		for _, r := range e.daemon.Requests() {
			if strings.HasSuffix(r.Path, "/start") {
				t.Fatalf("%s: the container was started", name)
			}
		}
		if len(e.daemon.Containers()) != 0 {
			t.Fatalf("%s: the container was left behind", name)
		}
	}
}
