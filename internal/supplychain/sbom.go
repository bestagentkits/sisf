// Package supplychain produces and checks the supply-chain evidence of a
// release: a CycloneDX SBOM built from `go version -m` output and the image
// digest, an in-toto provenance statement, signing and verification of
// deploy artifacts, and the hard gate that holds back a candidate whose
// dependency, lockfile, base-image or CI-configuration change is unpinned,
// has known vulnerabilities, or adds a dependency the Owner has not approved.
package supplychain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// CycloneDX format of the generated SBOM.
const (
	cycloneDXFormat  = "CycloneDX"
	cycloneDXVersion = "1.6"
)

// maxGoVersionOutput bounds the `go version -m` text accepted.
const maxGoVersionOutput = 8 << 20

// SBOMInput is what an SBOM is built from.
type SBOMInput struct {
	// ImageName is the image repository, for example "ghcr.io/acme/app".
	ImageName string
	// ImageDigest is the digest of the image the binaries ship in.
	ImageDigest core.Digest
	// GoVersionM is the output of `go version -m` over the image's Go
	// binaries; it may cover more than one binary.
	GoVersionM string
	// Timestamp is when the SBOM is made, from the caller's clock.
	Timestamp time.Time
}

// ParseGoVersionM reads the output of `go version -m`. Each binary opens
// with an unindented "<file>: <go version>" line followed by its indented
// build information. Output that names no binary, or a binary whose build
// information is unreadable, is INVALID_ARGUMENT.
func ParseGoVersionM(output string) ([]*debug.BuildInfo, error) {
	if len(output) > maxGoVersionOutput {
		return nil, invalid("go version -m output is larger than %d bytes", maxGoVersionOutput)
	}
	var (
		infos     []*debug.BuildInfo
		current   *strings.Builder
		goVersion string
	)
	flush := func() error {
		if current == nil {
			return nil
		}
		info, err := debug.ParseBuildInfo(current.String())
		if err == nil {
			// The parser skips the toolchain line; it comes from the header.
			info.GoVersion = goVersion
		}
		if err != nil {
			return invalid("go version -m output holds unreadable build information").WithCause(err)
		}
		if info.Path == "" || info.Main.Path == "" {
			return invalid("go version -m output holds a binary without a main module")
		}
		infos = append(infos, info)
		return nil
	}
	for line := range strings.SplitSeq(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		if indented, ok := strings.CutPrefix(line, "\t"); ok {
			if current == nil {
				return nil, invalid("go version -m output has build information before any binary")
			}
			current.WriteString(indented + "\n")
			continue
		}
		cut := strings.LastIndex(line, ": go")
		if cut < 0 {
			return nil, invalid("go version -m output has a line that names no binary")
		}
		if err := flush(); err != nil {
			return nil, err
		}
		current = &strings.Builder{}
		goVersion = strings.TrimSpace(line[cut+2:])
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, invalid("go version -m output names no binary")
	}
	return infos, nil
}

// GenerateSBOM returns a CycloneDX JSON SBOM of an image: the image as the
// described component, each Go binary's main module as an application and
// every module they link as a library, with its module hash. The same input
// always yields the same bytes.
func GenerateSBOM(in SBOMInput) ([]byte, error) {
	infos, err := ParseGoVersionM(in.GoVersionM)
	if err != nil {
		return nil, err
	}
	return sbomOf(in.ImageName, in.ImageDigest, in.Timestamp, infos, nil)
}

// sbomOf renders the CycloneDX SBOM of the image name@digest made at
// timestamp, whose Go binaries have the build information infos; props
// are added to the described component.
func sbomOf(name string, digest core.Digest, timestamp time.Time, infos []*debug.BuildInfo, props []cdxProperty) ([]byte, error) {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "@ \t\n") {
		return nil, invalid("the SBOM needs the image name without a digest")
	}
	if err := digest.Validate(); err != nil {
		return nil, err
	}
	if timestamp.IsZero() {
		return nil, invalid("the SBOM needs a timestamp")
	}
	image := cdxComponent{
		Type: "container", BOMRef: name + "@" + string(digest),
		Name: name, Version: string(digest), Hashes: digestHashes(digest), Properties: props,
	}
	components := map[string]cdxComponent{}
	dependsOn := map[string][]string{}
	var apps []string
	for _, info := range infos {
		app := moduleComponent("application", info.Main)
		app.Properties = []cdxProperty{{Name: "sisf:go_version", Value: info.GoVersion}, {Name: "sisf:package", Value: info.Path}}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" || s.Key == "CGO_ENABLED" {
				app.Properties = append(app.Properties, cdxProperty{Name: "sisf:build:" + s.Key, Value: s.Value})
			}
		}
		components[app.BOMRef] = app
		apps = append(apps, app.BOMRef)
		for _, dep := range info.Deps {
			lib := moduleComponent("library", *dep)
			components[lib.BOMRef] = lib
			dependsOn[app.BOMRef] = append(dependsOn[app.BOMRef], lib.BOMRef)
		}
	}
	bom := cdxBOM{
		BOMFormat: cycloneDXFormat, SpecVersion: cycloneDXVersion, Version: 1,
		Metadata: cdxMetadata{Timestamp: timestamp.UTC().Format(time.RFC3339), Component: image},
	}
	for _, ref := range slices.Sorted(maps.Keys(components)) {
		bom.Components = append(bom.Components, components[ref])
	}
	bom.Dependencies = append(bom.Dependencies, cdxDependency{Ref: image.BOMRef, DependsOn: sortedUnique(apps)})
	for _, ref := range slices.Sorted(maps.Keys(dependsOn)) {
		bom.Dependencies = append(bom.Dependencies, cdxDependency{Ref: ref, DependsOn: sortedUnique(dependsOn[ref])})
	}
	unnamed, err := json.Marshal(bom)
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "the SBOM cannot be encoded").WithCause(err)
	}
	bom.SerialNumber = serialNumber(unnamed)
	out, err := json.MarshalIndent(bom, "", "  ")
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "the SBOM cannot be encoded").WithCause(err)
	}
	return append(out, '\n'), nil
}

// moduleComponent describes one Go module. A replaced module is described
// as its replacement, the code that was actually linked.
func moduleComponent(kind string, m debug.Module) cdxComponent {
	linked := m
	var props []cdxProperty
	if m.Replace != nil {
		linked = *m.Replace
		props = append(props, cdxProperty{Name: "sisf:replaces", Value: m.Path + "@" + m.Version})
	}
	version := linked.Version
	if version == "" {
		version = "(devel)"
	}
	purl := "pkg:golang/" + linked.Path + "@" + version
	c := cdxComponent{Type: kind, BOMRef: purl, Name: linked.Path, Version: version, PURL: purl, Properties: props}
	if hash, ok := moduleHash(linked.Sum); ok {
		c.Hashes = []cdxHash{{Alg: "SHA-256", Content: hash}}
	}
	return c
}

// moduleHash turns a Go module sum "h1:<base64>" into the hex SHA-256 it holds.
func moduleHash(sum string) (string, bool) {
	encoded, ok := strings.CutPrefix(sum, "h1:")
	if !ok {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != sha256.Size {
		return "", false
	}
	return hex.EncodeToString(raw), true
}

// digestHashes renders a digest as a CycloneDX hash.
func digestHashes(d core.Digest) []cdxHash {
	alg, value, _ := strings.Cut(string(d), ":")
	return []cdxHash{{Alg: strings.ToUpper(alg[:3]) + "-" + alg[3:], Content: value}}
}

// serialNumber derives a stable RFC 4122 URN from the SBOM's content.
func serialNumber(content []byte) string {
	sum := sha256.Sum256(content)
	u := sum[:16]
	u[6] = (u[6] & 0x0f) | 0x50 // name-based
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

func sortedUnique(values []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(values)))
}

// The CycloneDX document, limited to the fields the SBOM fills.
type (
	cdxBOM struct {
		BOMFormat    string          `json:"bomFormat"`
		SpecVersion  string          `json:"specVersion"`
		SerialNumber string          `json:"serialNumber,omitempty"`
		Version      int             `json:"version"`
		Metadata     cdxMetadata     `json:"metadata"`
		Components   []cdxComponent  `json:"components"`
		Dependencies []cdxDependency `json:"dependencies"`
	}
	cdxMetadata struct {
		Timestamp string       `json:"timestamp"`
		Component cdxComponent `json:"component"`
	}
	cdxComponent struct {
		Type       string        `json:"type"`
		BOMRef     string        `json:"bom-ref"`
		Name       string        `json:"name"`
		Version    string        `json:"version,omitempty"`
		PURL       string        `json:"purl,omitempty"`
		Hashes     []cdxHash     `json:"hashes,omitempty"`
		Properties []cdxProperty `json:"properties,omitempty"`
	}
	cdxHash struct {
		Alg     string `json:"alg"`
		Content string `json:"content"`
	}
	cdxProperty struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	cdxDependency struct {
		Ref       string   `json:"ref"`
		DependsOn []string `json:"dependsOn"`
	}
)

// DigestOf returns the sha256 digest of data, the form artifacts, SBOMs and
// provenance statements are referred to by.
func DigestOf(data []byte) core.Digest {
	sum := sha256.Sum256(data)
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

func invalid(format string, args ...any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, fmt.Sprintf(format, args...))
}
