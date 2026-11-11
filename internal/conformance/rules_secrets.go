package conformance

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkSecrets keeps secrets out of the manifest. The fields that name a
// secret are held to the reference form by the schema; adapter config and
// extensions have no schema, so there a secret is recognized two ways: by
// the key it stands under, and by its own shape whatever the key. The typed
// fields that hold free text, such as a unit's command or a zone's source,
// are searched by shape. A finding gives the place and the expected form and
// never the value.
func (c *checker) checkSecrets() {
	c.checkTypedTextSecrets()
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
	// toggle is true below a key that turns the use of a secret on or off,
	// such as use_credentials, where text must be a yes or a no.
	toggle bool
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
			if shape := secretShape(key); shape != "" {
				c.add(RuleSecretInline, path+"."+key, "remove the key; keep the secret with a secret provider and put "+manifest.SecretRefForm+" in a value",
					"a key below %s holds %s; a secret is never written in the manifest, as a key or as a value (the key is not shown)", path, shape)
				continue
			}
			c.checkInlineSecrets(path+"."+key, v[key], setting{
				secret:      above.secret || namesSecret(key),
				environment: above.environment || namesEnvironment(key),
				toggle:      above.toggle || namesSecretSwitch(key),
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
			at := fmt.Sprintf("%s[%d]", path, i)
			if c.flagValueIsSecret(at, v, i) {
				continue
			}
			c.checkInlineSecrets(at, item, above)
		}
	case string:
		const fix = "store the secret with a secret provider and put " + manifest.SecretRefForm + " here"
		switch shape := secretShape(v); {
		case manifest.IsSecretRef(v):
		case above.secret:
			c.add(RuleSecretInline, path, fix,
				"%s is named like a secret but does not hold %s; a secret is never written in the manifest (the value is not shown)", path, manifest.SecretRefForm)
		case above.toggle && !isBooleanWord(v):
			c.add(RuleSecretInline, path, "set "+path+" to true or false, and put the secret it turns on, as "+manifest.SecretRefForm+", in a setting of its own",
				"%s turns the use of a secret on or off, yet holds text other than a yes or a no, which may be the secret itself (the value is not shown)", path)
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

// flagValueIsSecret reports, and records, a list item that follows a flag
// named like a secret, as "opaque" follows "--token" in a list of arguments,
// and is not a reference.
func (c *checker) flagValueIsSecret(at string, items []any, i int) bool {
	if i == 0 {
		return false
	}
	flag, isText := items[i-1].(string)
	value, alsoText := items[i].(string)
	if !isText || !alsoText || !secretGivenToFlag(flag, value) {
		return false
	}
	c.add(RuleSecretInline, at, "store the secret with a secret provider and pass "+manifest.SecretRefForm+" to the flag, or a file that holds it",
		"%s follows a flag named like a secret, so it is that secret written in the manifest (the value is not shown)", at)
	return true
}

// assignsSecret reports whether text is NAME=value with a value that is a
// secret written in place of a reference (see holdsSecret).
func assignsSecret(text string) bool {
	name, value, assigned := strings.Cut(text, "=")
	return assigned && holdsSecret(strings.TrimSpace(name), value)
}
