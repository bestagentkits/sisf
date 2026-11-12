package compose_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/compose"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

func TestStageStartsTheRevisionWithoutSendingItTraffic(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.stage(digestA)

	if rev.Unit != "app" || rev.Digest != digestA || rev.ID != name(digestA) {
		t.Fatalf("revision = %+v", rev)
	}
	if !w.running(name(digestA)) {
		t.Fatal("the staged container is not running")
	}
	if split, _ := w.splitter.Split(context.Background()); split.Stable.Revision != "bootstrap" || split.Canary != nil {
		t.Fatalf("staging changed the edge: %+v", split)
	}
	c := w.daemon.Containers()[0]
	if c.Labels[compose.LabelDigest] != string(digestA) || c.Labels[compose.LabelUnit] != "app" || c.Labels[compose.LabelFactory] != string(factory) {
		t.Errorf("labels = %v", c.Labels)
	}
	if !slices.Contains(c.Env, compose.RevisionEnv+"="+string(digestA)) {
		t.Errorf("env = %v, want the revision set", c.Env)
	}
}

func TestStagedContainersGetNoSocketNoPrivilegeAndNoHostPath(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.stage(digestA)

	created := w.daemon.Created()
	if len(created) != 1 {
		t.Fatalf("%d containers created, want 1", len(created))
	}
	raw := string(created[0].Body)
	for _, forbidden := range []string{"docker.sock", "DOCKER_HOST", `"Binds"`, `"Privileged":true`, `"Devices"`, `"PidMode"`, `"NetworkMode":"host"`} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the create request contains %s:\n%s", forbidden, raw)
		}
	}
	var body struct {
		HostConfig struct {
			NetworkMode    string
			ReadonlyRootfs bool
			CapDrop        []string
			SecurityOpt    []string
			Mounts         []struct{ Type, Source, Target string }
			Memory         int64
			MemorySwap     int64
			NanoCpus       int64
			PidsLimit      int64
		}
	}
	if err := json.Unmarshal(created[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	h := body.HostConfig
	if h.NetworkMode != "acme-app_default" || !h.ReadonlyRootfs || !slices.Contains(h.CapDrop, "ALL") || !slices.Contains(h.SecurityOpt, "no-new-privileges") {
		t.Errorf("host config = %+v", h)
	}
	if len(h.Mounts) != 1 || h.Mounts[0].Type != "volume" || h.Mounts[0].Source != "acme-app_data" {
		t.Errorf("mounts = %+v, want only the named data volume", h.Mounts)
	}
	want := compose.DefaultResourceLimits
	if h.Memory != want.MemoryBytes || h.MemorySwap != want.MemoryBytes || h.NanoCpus != want.NanoCPUs || h.PidsLimit != want.Pids {
		t.Errorf("resource limits = %d/%d bytes, %d nano CPUs, %d processes; want %+v", h.Memory, h.MemorySwap, h.NanoCpus, h.PidsLimit, want)
	}
}

func TestARevisionRunsWithinTheConfiguredResourceLimits(t *testing.T) {
	t.Parallel()
	limits := compose.ResourceLimits{MemoryBytes: 256 << 20, NanoCPUs: 500_000_000, Pids: 128}
	w := newWorld(t, worldOptions{cfg: func(c *compose.Config) { c.Limits = limits }})
	w.stage(digestA)
	var body struct {
		HostConfig struct{ Memory, MemorySwap, NanoCpus, PidsLimit int64 }
	}
	if err := json.Unmarshal(w.daemon.Created()[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if h := body.HostConfig; h.Memory != limits.MemoryBytes || h.MemorySwap != limits.MemoryBytes || h.NanoCpus != limits.NanoCPUs || h.PidsLimit != limits.Pids {
		t.Fatalf("resource limits = %+v, want %+v", h, limits)
	}
}

func TestStagingTheSameRevisionTwiceRunsOneContainer(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.stage(digestA)
	w.stage(digestA)
	if n := len(w.daemon.Created()); n != 1 {
		t.Fatalf("%d containers created, want 1", n)
	}
}

func TestStageRefusesWhatItShouldNotRunBeforeTouchingDocker(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*deploy.Artifact){
		"another factory": func(a *deploy.Artifact) { a.FactoryID = "other-app" },
		"no factory":      func(a *deploy.Artifact) { a.FactoryID = "" },
		"image by tag":    func(a *deploy.Artifact) { a.Image = "ghcr.io/acme/app:latest" },
		"bad unit":        func(a *deploy.Artifact) { a.Unit = "App; rm" },
		"unsigned":        func(a *deploy.Artifact) { a.Signature = core.Signature{} },
		"short digest":    func(a *deploy.Artifact) { a.Digest = "sha256:abc" },
	} {
		w := newWorld(t, worldOptions{})
		a := artifact(digestA)
		mutate(&a)
		if _, err := w.backend.Stage(context.Background(), a); err == nil {
			t.Errorf("%s: Stage succeeded", name)
		}
		if n := len(w.daemon.Requests()); n != 0 {
			t.Errorf("%s: %d requests reached Docker", name, n)
		}
	}
}

func TestAContainerOfAnotherRevisionUnderTheSameNameIsNotReused(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.daemon.AddContainer(name(digestA), map[string]string{compose.LabelDigest: string(digestB), compose.LabelFactory: string(factory)})
	if _, err := w.backend.Stage(context.Background(), artifact(digestA)); !core.HasCode(err, core.CodeConflict) {
		t.Fatalf("Stage over a foreign container: %v, want CONFLICT", err)
	}
}

func TestAStagedApplicationThatNamesAnotherRevisionHoldsAndIsRemoved(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.setSay(name(digestA), string(digestB)) // the image is not what the artifact claims
	_, err := w.backend.Stage(context.Background(), artifact(digestA))
	if !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Stage: %v, want RELEASE_HOLD", err)
	}
	if got := w.containerNames(); len(got) != 0 {
		t.Fatalf("containers left behind: %v", got)
	}
}

func TestAStagedApplicationThatNeverBecomesHealthyHoldsWhenTheTimeIsUp(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{timeout: 10 * time.Second})
	w.setDown(name(digestA), true)
	start := w.clock.Now()
	_, err := w.backend.Stage(context.Background(), artifact(digestA))
	if !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Stage: %v, want RELEASE_HOLD", err)
	}
	if waited := w.clock.Now().Sub(start); waited < 10*time.Second {
		t.Errorf("gave up after %v, want it to wait out the timeout", waited)
	}
	if got := w.containerNames(); len(got) != 0 {
		t.Fatalf("containers left behind: %v", got)
	}
}

func TestPreviewIsOnlyForARevisionThatRunsAsItself(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	rev := w.stage(digestA)
	p, err := w.backend.Preview(context.Background(), rev)
	if err != nil || p.Revision != rev || !strings.HasPrefix(p.URL, "http://") {
		t.Fatalf("Preview = %+v, %v", p, err)
	}
	w.setSay(name(digestA), string(digestB))
	if _, err := w.backend.Preview(context.Background(), rev); !core.HasCode(err, core.CodeReleaseHold) {
		t.Fatalf("Preview of a revision that lies: %v, want RELEASE_HOLD", err)
	}
	if _, err := w.backend.Preview(context.Background(), deploy.Revision{Unit: "app", Digest: "latest"}); err == nil {
		t.Fatal("a malformed revision was previewed")
	}
}

func TestCapabilitiesAllowSelfPublishingBecauseBothFormsAreProven(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	caps, err := w.backend.Capabilities(context.Background())
	if err != nil || !deploy.SelfPublishAllowed(caps) || !caps.Supports(core.CapDeployCanary) || !caps.Supports(core.CapDeployBlueGreen) {
		t.Fatalf("capabilities = %v, %v", caps, err)
	}
}

func TestADockerEndpointThatIsDownIsUnavailableAndStagesNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t, worldOptions{})
	w.docker.Close()
	if _, err := w.backend.Stage(context.Background(), artifact(digestA)); !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("Stage: %v, want UNAVAILABLE", err)
	}
}
