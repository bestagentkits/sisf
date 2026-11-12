package manifest

import "regexp"

// secretRefPattern is the text form of a reference to a secret: "env:NAME",
// an environment variable, or "local:path/name", an entry of the factory's
// encrypted store. The provider set is closed, so a value that merely has a
// colon in it, such as "postgres:password", is not a reference; a provider
// is added here when its adapter exists.
const secretRefPattern = `^(env:[A-Z_][A-Z0-9_]*|local:[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*)$`

// SecretRefForm says in words what a secret reference looks like. Messages
// about a refused value use it in place of the value, which may be a secret.
const SecretRefForm = `a reference written "env:NAME" or "local:path/name"`

var secretRef = regexp.MustCompile(secretRefPattern)

// IsSecretRef reports whether s names a secret rather than holding one.
func IsSecretRef(s string) bool { return secretRef.MatchString(s) }
