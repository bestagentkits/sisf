package conformance

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkSecrets keeps secrets out of the free-form parts of the manifest. The
// fields that name a secret are held to the reference form by the schema;
// adapter config and extensions have no schema, so there a secret is
// recognized two ways: by the key it stands under, and by its own shape
// whatever the key. A finding gives the place and the expected form and
// never the value.
func (c *checker) checkSecrets() {
	adapters := []struct {
		name   string
		config map[string]any
	}{
		{"coder", c.m.Adapters.Coder.Config}, {"qa", c.m.Adapters.QA.Config},
		{"execution", c.m.Adapters.Execution.Config}, {"deploy", c.m.Adapters.Deploy.Config},
	}
	for _, adapter := range adapters {
		c.checkInlineSecrets("adapters."+adapter.name+".config", adapter.config, setting{})
	}
	c.checkInlineSecrets("extensions", c.m.Extensions, setting{})
}

// setting says what the keys above a free-form value tell about it.
type setting struct {
	// secret is true below a key that names a secret, where every text
	// value must be a reference.
	secret bool
	// environment is true below a key that lists environment variables,
	// where text may be an assignment such as NAME=value.
	environment bool
}

// checkInlineSecrets walks one free-form value.
func (c *checker) checkInlineSecrets(path string, value any, above setting) {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			c.checkInlineSecrets(path+"."+key, v[key], setting{
				secret:      above.secret || namesSecret(key),
				environment: above.environment || namesEnvironment(key),
			})
		}
	case map[any]any:
		// A mapping with a key that is not text has no JSON form and is
		// refused by the schema; its text keys are still judged here.
		named := map[string]any{}
		for key, inner := range v {
			if text, ok := key.(string); ok {
				named[text] = inner
			}
		}
		c.checkInlineSecrets(path, named, above)
	case []any:
		for i, item := range v {
			c.checkInlineSecrets(fmt.Sprintf("%s[%d]", path, i), item, above)
		}
	case string:
		const fix = "store the secret with a secret provider and put " + manifest.SecretRefForm + " here"
		switch shape := secretShape(v); {
		case manifest.IsSecretRef(v):
		case above.secret:
			c.add(RuleSecretInline, path, fix,
				"%s is named like a secret but does not hold %s; a secret is never written in the manifest (the value is not shown)", path, manifest.SecretRefForm)
		case shape != "":
			c.add(RuleSecretInline, path, fix,
				"%s holds %s; a secret is never written in the manifest, whatever the setting is called (the value is not shown)", path, shape)
		case above.environment && assignsSecret(v):
			c.add(RuleSecretInline, path,
				"store the secret with a secret provider and assign "+manifest.SecretRefForm+" to the variable",
				"%s assigns a value to an environment variable named like a secret; a secret is never written in the manifest (the value is not shown)", path)
		}
	}
}

// The shapes a secret has whatever key it stands under.
var (
	// privateKeyBlock is the armour line of a private key in PEM, OpenSSH or
	// PGP form. A certificate or a public key is not a secret.
	privateKeyBlock = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`)
	// urlWithPassword is a URL whose authority carries "user:password@", as
	// a connection string does.
	urlWithPassword = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s/?#@:]*:[^\s/?#@]+@`)
)

// secretShape says what kind of secret a text value is by its shape, or ""
// when it has none of the known shapes.
func secretShape(value string) string {
	switch {
	case privateKeyBlock.MatchString(value):
		return "a private key block"
	case urlWithPassword.MatchString(value):
		return "a URL with a password in it"
	}
	return ""
}

// assignsSecret reports whether text is NAME=value with a name that says
// the value is a secret and a value that is not a reference.
func assignsSecret(text string) bool {
	name, value, assigned := strings.Cut(text, "=")
	return assigned && value != "" && !manifest.IsSecretRef(value) && namesSecret(strings.TrimSpace(name))
}
