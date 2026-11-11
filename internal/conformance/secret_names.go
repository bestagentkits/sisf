package conformance

import (
	"slices"
	"strings"
	"unicode"
)

// secretWords are the words that mark a key as holding a secret wherever
// they stand in it. They are written in the singular; a key word is also
// tried without its final "s", so "credentials" and "apikeys" match.
var secretWords = []string{"password", "passwd", "pwd", "passphrase", "secret", "credential", "apikey", "privatekey",
	"secretkey", "accesskey", "dsn", "authorization", "connectionstring", "connstr", "bearer"}

// keyQualifiers are the words that make a key ending in "key" a secret: an
// api key or a private key is one, a cache key or a sort key is not.
var keyQualifiers = []string{"api", "private", "secret", "access", "signing", "auth", "encryption", "ssh"}

// formWords say in which form a value is written, not what it is:
// private_key_pem holds what private_key holds, and api_key_ref names a
// secret whose value must be the reference.
var formWords = []string{"ref", "pem", "der", "b64", "base64", "hex", "value", "data", "material"}

// locatorWords end a key whose value says where a secret is kept rather
// than being one: tls_secret_name names a platform secret, credentials_file
// a path, password_stdin the input it is read from. Such a value is still
// judged by its shape.
var locatorWords = []string{"name", "file", "path", "dir", "stdin"}

// descriptorWords end a key that describes how a secret is found, checked
// or used rather than holding one: authorization_endpoint, token_url,
// credential_helper, secret_provider, credentials_mode, password_policy,
// password_min_length, and password_env or dsn_env, which name the variable
// that holds it. Such a value is still judged by its shape.
var descriptorWords = []string{"endpoint", "url", "uri", "helper", "provider", "mode", "policy", "env", "length"}

// switchWords begin a key that turns the use of a secret on or off, as
// use_credentials and require_password do; such a key holds a yes or a no.
var switchWords = []string{"use", "enable", "disable", "require", "allow"}

// booleanWords are the texts a switch may hold.
var booleanWords = []string{"true", "false", "yes", "no", "on", "off", "y", "n", "1", "0", "enabled", "disabled"}

// quantityWords make a key ending in "tokens" a count of model tokens
// rather than a credential, as in max_tokens and output_tokens.
var quantityWords = []string{"max", "min", "num", "number", "count", "total", "limit", "budget", "input", "output",
	"prompt", "completion", "context", "thinking", "reasoning"}

// singular returns a key word without the final "s" of a plural.
func singular(word string) string { return strings.TrimSuffix(word, "s") }

// namesSecret reports whether a config key says its value is a secret, as
// api_key, apiKey, github-token, db.password and private_key_pem do. Only
// text is judged below such a key, so a count such as max_tokens: 4096 is
// left alone, and so is the same count written as text.
func namesSecret(key string) bool {
	words := keyWords(key)
	return len(words) > 0 && !slices.Contains(switchWords, words[0]) && secretByName(words)
}

// namesSecretSwitch reports whether a config key turns the use of a secret
// on or off, as use_credentials does: it begins with a switch word and would
// name a secret without it.
func namesSecretSwitch(key string) bool {
	words := keyWords(key)
	return len(words) > 1 && slices.Contains(switchWords, words[0]) && secretByName(words[1:])
}

// isBooleanWord reports whether text is a yes or a no.
func isBooleanWord(text string) bool {
	return slices.Contains(booleanWords, strings.ToLower(strings.TrimSpace(text)))
}

// secretByName reports whether the words of a key name a secret.
func secretByName(words []string) bool {
	for len(words) > 0 && slices.Contains(formWords, words[len(words)-1]) {
		words = words[:len(words)-1]
	}
	n := len(words)
	if n == 0 || slices.Contains(locatorWords, words[n-1]) || slices.Contains(descriptorWords, words[n-1]) {
		return false
	}
	for i, word := range words {
		if slices.Contains(secretWords, word) || slices.Contains(secretWords, singular(word)) {
			return true
		}
		// "connection string" is one name in two words.
		if i > 0 && (word == "string" || word == "str") && (words[i-1] == "connection" || words[i-1] == "conn") {
			return true
		}
	}
	// A token is a secret where the key ends in it: token_endpoint and
	// jwt_issuer describe one, github_token, service_jwt and github_pat (a
	// personal access token) hold one.
	switch last := singular(words[n-1]); last {
	case "token", "jwt", "pat":
		return !slices.ContainsFunc(words[:n-1], func(word string) bool { return slices.Contains(quantityWords, word) })
	case "key":
		return n > 1 && slices.Contains(keyQualifiers, words[n-2])
	}
	return false
}

// namesEnvironment reports whether a config key lists environment
// variables, as env, environment and env_vars do.
func namesEnvironment(key string) bool {
	return slices.ContainsFunc(keyWords(key), func(word string) bool {
		return word == "env" || word == "envs" || word == "environment"
	})
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
