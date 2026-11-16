package managedapp

import (
	"slices"
	"strings"
	"testing"
)

func TestARevisionReadsItsSecretsFromFilesOnly(t *testing.T) {
	t.Parallel()
	s, err := Load("../../examples/woocommerce-app/" + FileName)
	if err != nil {
		t.Fatal(err)
	}
	env := s.Runtime.RevisionEnv()
	for _, want := range []string{"WORDPRESS_DB_HOST=db", "WORDPRESS_DB_PASSWORD_FILE=/run/secrets/db_password", "WORDPRESS_SECRET_FILE=/run/secrets/wp_secret"} {
		if !slices.Contains(env, want) {
			t.Errorf("env %v lacks %s", env, want)
		}
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "WORDPRESS_DB_PASSWORD=") || strings.HasPrefix(kv, "WORDPRESS_SECRET=") {
			t.Errorf("a secret is in the environment as a value: %s", kv)
		}
	}
	if !slices.IsSorted(env) || !slices.Equal(env, s.Runtime.RevisionEnv()) {
		t.Errorf("the environment is not stable: %v", env)
	}
}
