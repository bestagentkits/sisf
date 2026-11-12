// Package egress enforces and governs the outbound network policy of agent
// sandboxes.
//
// Proxy is the CONNECT and plain-HTTP forward proxy runners reach the
// outside through: it lets a connection through only to a host the policy
// allows, on an allowed port, at an address that is public, and logs every
// blocked destination for the Owner to review. Service is the only way the
// policy changes: plan, Owner approval, execute, audit. An agent cannot
// widen it, and widening or turning off blocking carries a risk warning.
package egress

import (
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Groups of hosts in the default allowlist: what development needs and
// nothing more. Model providers are added for the providers configured.
var (
	// GitHubHosts serve repositories, the API, raw content and release assets.
	GitHubHosts = []string{"github.com", "api.github.com", "codeload.github.com", "*.githubusercontent.com"}
	// PackageRegistryHosts serve npm, PyPI, crates.io, the Go module proxy
	// and checksum database, and Maven Central.
	PackageRegistryHosts = []string{
		"registry.npmjs.org",
		"pypi.org", "files.pythonhosted.org",
		"crates.io", "static.crates.io", "index.crates.io",
		"proxy.golang.org", "sum.golang.org",
		"repo.maven.apache.org", "repo1.maven.org",
	}
	// ImageRegistryHosts serve Docker Hub and the GitHub Container Registry.
	ImageRegistryHosts = []string{"registry-1.docker.io", "auth.docker.io", "production.cloudflare.docker.com", "ghcr.io"}
	// OSPackageHosts serve Debian, Ubuntu and Alpine packages.
	OSPackageHosts = []string{
		"deb.debian.org", "security.debian.org",
		"archive.ubuntu.com", "security.ubuntu.com", "ports.ubuntu.com",
		"dl-cdn.alpinelinux.org",
	}
)

// providerHosts maps a model provider name, as model routes name it, to the
// API hosts it is reached at.
var providerHosts = map[string][]string{
	"anthropic":  {"api.anthropic.com"},
	"openai":     {"api.openai.com"},
	"google":     {"generativelanguage.googleapis.com"},
	"gemini":     {"generativelanguage.googleapis.com"},
	"mistral":    {"api.mistral.ai"},
	"deepseek":   {"api.deepseek.com"},
	"xai":        {"api.x.ai"},
	"openrouter": {"openrouter.ai"},
}

// ProviderHosts returns the API hosts of a model provider, or nil for a
// provider this package does not know.
func ProviderHosts(provider string) []string {
	return slices.Clone(providerHosts[strings.ToLower(strings.TrimSpace(provider))])
}

// DefaultPolicy is the allowlist a factory starts with: the API hosts of
// the configured model providers, GitHub, the package registries, the image
// registries and the operating-system package repositories. A provider
// this package does not know is INVALID_ARGUMENT; its hosts are an Owner
// decision to add through Service.
func DefaultPolicy(providers ...string) (core.EgressPolicy, error) {
	var allow []string
	for _, group := range [][]string{GitHubHosts, PackageRegistryHosts, ImageRegistryHosts, OSPackageHosts} {
		allow = append(allow, group...)
	}
	for _, p := range providers {
		hosts := ProviderHosts(p)
		if hosts == nil {
			return core.EgressPolicy{}, core.NewError(core.CodeInvalidArgument,
				"model provider "+p+" has no known API host; the Owner adds it to the egress policy")
		}
		allow = append(allow, hosts...)
	}
	policy := Normalize(core.EgressPolicy{Mode: core.EgressAllowlist, Allow: allow})
	return policy, policy.Validate()
}

// Normalize sorts the allowlist and removes duplicates, so two policies
// that allow the same hosts compare and digest alike.
func Normalize(p core.EgressPolicy) core.EgressPolicy {
	allow := slices.Clone(p.Allow)
	slices.Sort(allow)
	return core.EgressPolicy{Mode: p.Mode, Allow: slices.Compact(allow)}
}
