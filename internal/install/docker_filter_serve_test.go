package install

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/dockerfilter"
)

var filterImage = "sha256:" + strings.Repeat("b", 64)

func TestEachDockerFilterTakesOnlyTheSettingsOfItsRole(t *testing.T) {
	t.Parallel()
	base := DockerFilterOptions{Listen: "127.0.0.1:0", Socket: "/var/run/docker.sock", FactoryID: "sisf-acme", Network: "sisf-acme-sandbox", Images: []string{filterImage}}
	runner, deploy := base, base
	runner.Role, deploy.Role = dockerfilter.RoleRunner, dockerfilter.RoleDeploy
	deploy.SimulatedImages = map[string]string{"app": filterImage}
	for name, o := range map[string]DockerFilterOptions{"runner": runner, "deploy": deploy} {
		if _, err := o.filter(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	simRunner := runner
	simRunner.SimulatedImages = map[string]string{"app": filterImage}
	unknown := base
	unknown.Role = "admin"
	noRole := base
	unpinned := deploy
	unpinned.Images = []string{"hello:latest"}
	for name, o := range map[string]DockerFilterOptions{
		"a runner with simulated images": simRunner, "an unknown role": unknown, "no role": noRole, "an unpinned image": unpinned,
	} {
		if _, err := o.filter(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestADockerFilterServesUntilItsContextEnds(t *testing.T) {
	t.Parallel()
	o := DockerFilterOptions{Role: dockerfilter.RoleRunner, Listen: "127.0.0.1:0", Socket: "/nonexistent.sock",
		FactoryID: "sisf-acme", Network: "sisf-acme-sandbox", Images: []string{filterImage}}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := ServeDockerFilter(ctx, o); err != nil {
		t.Fatalf("the filter did not stop cleanly: %v", err)
	}
	o.Listen = "not an address"
	if err := ServeDockerFilter(t.Context(), o); err == nil {
		t.Fatal("a filter served on an address it cannot listen on")
	}
}
