package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestTheStatusListsTheHeldBuildsOrSaysTheyAreUnknown(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	held := HeldBuild{IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, RunID: "run-0001", Attempt: 2,
		Failures: 5, LastCode: core.CodeUnavailable, Since: since}
	client, _ := serveActions(t, Options{HeldBuilds: func(context.Context) ([]HeldBuild, error) { return []HeldBuild{held}, nil }})
	status, err := client.Status(context.Background())
	if err != nil || len(status.HeldBuilds) != 1 || status.HeldBuilds[0] != held || status.HeldBuildsUnknown {
		t.Fatalf("status = %+v, %v", status, err)
	}
	client, _ = serveActions(t, Options{HeldBuilds: func(context.Context) ([]HeldBuild, error) { return nil, errors.New("store down") }})
	if status, err := client.Status(context.Background()); err != nil || !status.HeldBuildsUnknown || len(status.HeldBuilds) != 0 {
		t.Fatalf("with the store down: %+v, %v", status, err)
	}
	client, _ = serveActions(t, Options{})
	if status, err := client.Status(context.Background()); err != nil || status.HeldBuildsUnknown || status.HeldBuilds != nil {
		t.Fatalf("without the lifecycle: %+v, %v", status, err)
	}
}
