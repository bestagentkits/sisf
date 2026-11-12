package main

import (
	"context"
	"flag"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestTheBuildOutageLimitDefaultsToFiveAndMustBePositive(t *testing.T) {
	t.Parallel()
	var l lifecycleFlags
	fs := flag.NewFlagSet("supervisor", flag.ContinueOnError)
	l.register(fs)
	if err := fs.Parse(nil); err != nil || l.buildOutageLimit != 5 || stages.DefaultBuildRetry.HoldAfter != 5 {
		t.Fatalf("the default limit is %d (%v)", l.buildOutageLimit, err)
	}
	for _, v := range []string{"0", "-1"} {
		l := lifecycleFlags{factoryManifest: "/etc/sisf/factory.yaml"}
		fs := flag.NewFlagSet("supervisor", flag.ContinueOnError)
		l.register(fs)
		if err := fs.Parse([]string{"--build-outage-limit=" + v}); err != nil {
			t.Fatal(err)
		}
		_, err := l.buildLifecycle(context.Background(), lifecycleDeps{})
		if !core.HasCode(err, core.CodeInvalidArgument) || !strings.Contains(err.Error(), "--build-outage-limit") {
			t.Errorf("a limit of %s: %v", v, err)
		}
	}
}
