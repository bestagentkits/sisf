package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/cmd/supervisor/observe"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The values the control zone's env secret provider serves are secrets, so
// the process's log never prints them, whatever they are named.
func TestControlSecretValuesAreRedactedFromTheLog(t *testing.T) {
	t.Parallel()
	const value = "control-secret-value-1234"
	env := map[string]string{wiring.EnvControlSecretPrefix + "DB_PASSWORD": value, wiring.EnvControlSecretPrefix + "EMPTY": "  "}
	getenv := func(k string) string { return env[k] }
	environ := func() []string {
		out := []string{"PATH=/usr/bin", "NOT_A_SECRET=plain-value"}
		for k, v := range env {
			out = append(out, k+"="+v)
		}
		return out
	}
	secrets := logSecrets(getenv, environ)
	if len(secrets) != 1 || secrets[0] != value {
		t.Fatalf("log secrets = %q", secrets)
	}
	var buf bytes.Buffer
	proc, err := observe.Start(&buf, getenv, core.SystemClock{}, secrets...)
	if err != nil {
		t.Fatal(err)
	}
	proc.Log.Info("resolved a reference", "value", value, "other", "plain-value")
	if out := buf.String(); strings.Contains(out, value) || !strings.Contains(out, "plain-value") {
		t.Fatalf("the log: %q", out)
	}
}
