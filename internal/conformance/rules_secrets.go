package conformance

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// checkSecrets keeps secrets out of the free-form parts of the manifest. The
// fields that name a secret are held to the reference form by the schema;
// adapter config and extensions have no schema, so a setting whose key says
// it holds a secret must carry a reference too. A finding gives the place
// and the expected form and never the value.
func (c *checker) checkSecrets() {
	adapters := []struct {
		name   string
		config map[string]any
	}{
		{"coder", c.m.Adapters.Coder.Config}, {"qa", c.m.Adapters.QA.Config},
		{"execution", c.m.Adapters.Execution.Config}, {"deploy", c.m.Adapters.Deploy.Config},
	}
	for _, adapter := range adapters {
		c.checkInlineSecrets("adapters."+adapter.name+".config", adapter.config, false)
	}
	c.checkInlineSecrets("extensions", c.m.Extensions, false)
}

// checkInlineSecrets walks one free-form value. secret is true below a key
// that names a secret, where every text value must be a reference.
func (c *checker) checkInlineSecrets(path string, value any, secret bool) {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			c.checkInlineSecrets(path+"."+key, v[key], secret || namesSecret(key))
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
		c.checkInlineSecrets(path, named, secret)
	case []any:
		for i, item := range v {
			c.checkInlineSecrets(fmt.Sprintf("%s[%d]", path, i), item, secret)
		}
	case string:
		if secret && !manifest.IsSecretRef(v) {
			c.add(RuleSecretInline, path,
				"store the secret with a secret provider and put "+manifest.SecretRefForm+" here",
				"%s is named like a secret but does not hold %s; a secret is never written in the manifest (the value is not shown)", path, manifest.SecretRefForm)
		}
	}
}

// secretWords are the words that mark a key as holding a secret wherever
// they stand in it.
var secretWords = []string{"password", "passwd", "passphrase", "secret", "secrets", "credential", "credentials",
	"apikey", "dsn", "authorization"}

// keyQualifiers are the words that make a key ending in "key" a secret: an
// api key or a private key is one, a cache key or a sort key is not.
var keyQualifiers = []string{"api", "private", "secret", "access", "signing", "auth", "encryption", "ssh"}

// namesSecret reports whether a config key says its value is a secret, as
// api_key, apiKey, github-token and db.password do. Only text is judged
// below such a key, so a count such as max_tokens: 4096 is left alone.
func namesSecret(key string) bool {
	words := keyWords(key)
	if n := len(words); n > 0 && words[n-1] == "ref" {
		words = words[:n-1] // api_key_ref still names a secret; its value must be the reference
	}
	n := len(words)
	if n == 0 {
		return false
	}
	if slices.ContainsFunc(words, func(word string) bool { return slices.Contains(secretWords, word) }) {
		return true
	}
	last := strings.TrimSuffix(words[n-1], "s") // api_keys and tokens hold what api_key and token hold
	return last == "token" || last == "key" && n > 1 && slices.Contains(keyQualifiers, words[n-2])
}

// keyWords splits a key into lowercase words at separators and at the
// capitals of camelCase.
func keyWords(key string) []string {
	var words []string
	var word []rune
	flush := func() {
		if len(word) > 0 {
			words = append(words, strings.ToLower(string(word)))
			word = nil
		}
	}
	runes := []rune(key)
	for i, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
			continue
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1])):
			flush()
		}
		word = append(word, r)
	}
	flush()
	return words
}
