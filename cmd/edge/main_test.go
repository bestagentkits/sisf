package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestARunWithoutAnUpstreamIsAUsageError(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	if got := run(context.Background(), nil, &stderr); got != exitUsage {
		t.Fatalf("exit = %d, want %d", got, exitUsage)
	}
	if !strings.Contains(stderr.String(), "--upstream") {
		t.Fatalf("usage message does not name the missing flag: %q", stderr.String())
	}
}

func TestAPublicListenAddressNeedsAnExplicitAllowance(t *testing.T) {
	t.Parallel()
	for _, listen := range []string{":7460", "0.0.0.0:7460", "192.0.2.10:7460"} {
		var stderr bytes.Buffer
		args := []string{"--listen", listen, "--upstream", "http://app:7461"}
		if got := run(context.Background(), args, &stderr); got != exitUsage {
			t.Errorf("--listen %s: exit = %d, want %d", listen, got, exitUsage)
		}
	}
}

func TestARouteThatIsNotValidIsAUsageError(t *testing.T) {
	t.Parallel()
	args := []string{"--upstream", "http://app:7461", "--route", "report=http://control:7463"}
	if got := run(context.Background(), args, io.Discard); got != exitUsage {
		t.Fatalf("exit = %d, want %d", got, exitUsage)
	}
}
