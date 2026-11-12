package observe

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

func TestNeitherTheLogNorTheTelemetryCarriesASecretOfTheProcess(t *testing.T) {
	t.Parallel()
	const token = "edge-admin-0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	var out bytes.Buffer
	env := map[string]string{"EDGE_ADMIN_TOKEN": token}
	p, err := Start(&out, func(k string) string { return env[k] }, core.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	p.Log.Info("calling the edge", "authorization", "Bearer "+token)
	_, span := p.Telemetry.StartSpan(context.Background(), "release.step", telemetry.Attributes{
		FactoryID: "acme-hello", DeploymentID: "local", Zone: core.ZoneControl, RunID: "run-1",
		ReleaseDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
	})
	span.SetAttribute("detail", "token "+token)
	span.End()
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), token) {
		t.Fatalf("a secret was written: %s", out.String())
	}
	if !strings.Contains(out.String(), "calling the edge") {
		t.Fatalf("the log record is missing: %s", out.String())
	}
}
