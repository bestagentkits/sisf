package conformance

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The shapes a secret has whatever key or field it stands under.
var (
	// privateKeyBlock is the armour line of a private key in PEM, OpenSSH or
	// PGP form. A certificate or a public key is not a secret.
	privateKeyBlock = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`)
	// urlWithPassword is a URL whose authority carries "user:password@", as
	// a connection string does.
	urlWithPassword = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s/?#@:]*:[^\s/?#@]+@`)
	// urlAnywhere finds the URLs in a text.
	urlAnywhere = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)
	// queryParam finds the parameters of a query string, with or without
	// the URL around it, as in /hook?token=VALUE.
	queryParam = regexp.MustCompile(`[?&]([A-Za-z0-9_.-]+)=([^&#\s"'<>]+)`)
	// authorizationHeader is an HTTP header that carries a credential.
	authorizationHeader = regexp.MustCompile(`(?i)\b(proxy-)?authorization\s*:\s*\S`)
	// schemeCredential is a credential written with its HTTP scheme, as in
	// "Bearer <token>"; schemeCredentialValue tells a credential from a word
	// of prose such as "basic authentication".
	schemeCredential      = regexp.MustCompile(`(?i)\b(?:bearer|basic|digest)\s+([A-Za-z0-9._~+/=-]{8,})`)
	schemeCredentialValue = regexp.MustCompile(`[0-9._~+/=]`)
	// providerToken is a credential by the prefix its provider gives it:
	// GitHub tokens of every kind, GitLab and Slack tokens, model provider
	// API keys and cloud access key IDs.
	providerToken = regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{22,}|glpat-[A-Za-z0-9_-]{20,}|` +
		`xox[abposr]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{20,}|(AKIA|ASIA)[0-9A-Z]{16})`)
	// secretFlag is a command-line flag that names a secret and is given its
	// value after "=", as --token=VALUE is.
	secretFlag = regexp.MustCompile(`(^|\s)--?([A-Za-z][A-Za-z0-9_.-]*)=(\S+)`)
)

// signatureParams are query parameters of a URL that carry a credential
// although their names do not say "secret": a signature of a pre-signed
// URL, or an API key passed as key=.
var signatureParams = []string{"signature", "sig", "key", "credential", "auth"}

// secretShape says what kind of secret a text value is by its shape, or ""
// when it has none of the known shapes.
func secretShape(value string) string {
	switch {
	case privateKeyBlock.MatchString(value):
		return "a private key block"
	case urlWithPassword.MatchString(value):
		return "a URL with a password in it"
	case urlCarriesToken(value):
		return "a URL with a token in it"
	case authorizationHeader.MatchString(value) || carriesSchemeCredential(value):
		return "an Authorization credential"
	case providerToken.MatchString(value):
		return "a token in the form its provider issues"
	case flagSetsSecret(value):
		return "a command-line flag that sets a secret"
	}
	return ""
}

// carriesSchemeCredential reports whether text holds a credential after an
// HTTP authentication scheme. A credential has a digit or punctuation in it;
// a word of prose after "basic" or "bearer" has none.
func carriesSchemeCredential(text string) bool {
	for _, match := range schemeCredential.FindAllStringSubmatch(text, -1) {
		if schemeCredentialValue.MatchString(match[1]) {
			return true
		}
	}
	return false
}

// urlCarriesToken reports whether a URL in the text carries a credential in
// its user part, or a query string, with or without its URL, carries one. A
// user part that is a plain account name, as in ssh://git@host, carries
// none; one in the form of a provider's token does. A query parameter
// carries one when its name says so.
func urlCarriesToken(text string) bool {
	for _, raw := range urlAnywhere.FindAllString(text, -1) {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if parsed.User != nil && providerToken.MatchString(parsed.User.Username()) {
			return true
		}
	}
	for _, match := range queryParam.FindAllStringSubmatch(text, -1) {
		name, value := match[1], match[2]
		if unescaped, err := url.QueryUnescape(value); err == nil {
			value = unescaped
		}
		if queryNamesSecret(name) && !manifest.IsSecretRef(value) {
			return true
		}
	}
	return false
}

// queryNamesSecret reports whether a URL query parameter holds a secret by
// its name: one a config key would be refused under, or a signature.
func queryNamesSecret(name string) bool {
	if namesSecret(name) {
		return true
	}
	words := keyWords(name)
	return len(words) > 0 && slices.Contains(signatureParams, words[len(words)-1])
}

// flagSetsSecret reports whether text holds a flag named like a secret and
// given a value that is not a reference, such as --token=VALUE.
func flagSetsSecret(text string) bool {
	for _, match := range secretFlag.FindAllStringSubmatch(text, -1) {
		if namesSecret(match[2]) && !manifest.IsSecretRef(match[3]) {
			return true
		}
	}
	return false
}

// takesSecret reports whether a word is a flag named like a secret that
// takes its value from the next word, as "--token VALUE" does.
func takesSecret(word string) bool {
	name, isFlag := strings.CutPrefix(word, "-")
	return isFlag && !strings.Contains(word, "=") && namesSecret(strings.TrimLeft(name, "-"))
}

// givenSecret reports whether a word that follows a flag named like a
// secret is a value written in place of a reference: neither another flag
// nor a reference.
func givenSecret(word string) bool {
	return word != "" && !strings.HasPrefix(word, "-") && !manifest.IsSecretRef(word)
}
