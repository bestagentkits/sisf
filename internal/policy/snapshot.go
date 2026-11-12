package policy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Snapshot is the complete policy of a factory as read at one commit. Each
// load builds a new one, so nothing a reader does to its copy reaches the
// policy another reader gets.
type Snapshot struct {
	// Revision is the commit every file was read at.
	Revision core.CommitSHA
	// Digest identifies the exact content of all files together. It is what
	// the Owner approves when a revision becomes the baseline.
	Digest core.Digest
	// Manifest is .sisf/factory.yaml, parsed and checked against its schema.
	Manifest  *manifest.Manifest
	Agents    Document
	Review    Document
	Decisions Document
	// DecisionSchemaVersion is the version of the typed questions stated in
	// DECISIONS.md. A decision records it next to the commit.
	DecisionSchemaVersion int
	Catalog               Catalog
}

// Refs returns the references a decision made for a step of this kind
// stores, so it can be replayed later. The model route comes from the
// manifest; a step with no configured route is refused, never defaulted.
func (s *Snapshot) Refs(step core.StepKind) (core.PolicyRefs, error) {
	route, ok := s.Manifest.ModelRoutes.Routes[step]
	if !ok {
		return core.PolicyRefs{}, core.NewError(core.CodeInvalidArgument,
			fmt.Sprintf("the manifest configures no model route for step %q", string(step)))
	}
	refs := core.PolicyRefs{PolicySHA: s.Revision, ReviewSHA: s.Revision, DecisionSHA: s.Revision,
		FeatureCatalogSHA: s.Revision, ModelRoute: route}
	return refs, refs.Validate()
}

// Keys of the settings more than one statement can make.
const (
	keySchemaVersion = "decisions.schema_version"
	keyCatalog       = "features.catalog"
)

// schemaVersionPattern finds each place DECISIONS.md states the version of
// its typed questions, such as "schema version: `1`" or "(schema version 1)".
var schemaVersionPattern = regexp.MustCompile("(?i)schema version[\\s:=`]{0,4}(\\d+)")

// read loads and checks every foundation file at revision. It is the only
// way a Snapshot is made. Whether revision is approved is decided by the
// caller; read itself grants nothing.
func read(ctx context.Context, source Source, revision core.CommitSHA) (*Snapshot, error) {
	if err := revision.Validate(); err != nil {
		return nil, hold(reasonUnapproved, "policy is read only at a full commit hash, never at a branch or tag")
	}
	r := &reader{ctx: ctx, source: source, revision: revision, digests: map[string]core.Digest{}}
	s := &Snapshot{Revision: revision}
	var claims []Claim

	s.Agents = r.required(PathAgents)
	s.Review = r.required(PathReview)
	s.Decisions = r.required(PathDecisions)
	factory := r.required(PathFactory)
	markdown, hasMarkdown := r.optional(PathFeatures)
	structured, hasStructured := r.optional(PathFeaturesYAML)
	if r.err != nil {
		return nil, r.err
	}

	parsed, err := manifest.Parse([]byte(factory.Content))
	if err != nil {
		return nil, hold(reasonInvalid, "%s cannot be read: %s", PathFactory, messageOf(err)).WithCause(err)
	}
	if violations := manifest.Validate(parsed); len(violations) > 0 {
		return nil, hold(reasonInvalid, "%s breaks the manifest schema: %s", PathFactory, violations[0].String())
	}
	s.Manifest = parsed

	for _, match := range schemaVersionPattern.FindAllStringSubmatchIndex(s.Decisions.Content, -1) {
		line := 1 + strings.Count(s.Decisions.Content[:match[0]], "\n")
		claims = append(claims, Claim{Key: keySchemaVersion, Value: s.Decisions.Content[match[2]:match[3]],
			Layer: LayerOf(PathDecisions), Source: fmt.Sprintf("%s line %d", PathDecisions, line)})
	}

	if !hasMarkdown && !hasStructured {
		return nil, hold(reasonMissing, "neither %s nor %s exists at %s", PathFeatures, PathFeaturesYAML, revision)
	}
	if hasMarkdown {
		if s.Catalog, err = parseFeaturesMarkdown([]byte(markdown.Content)); err != nil {
			return nil, err
		}
		claims = append(claims, Claim{Key: keyCatalog, Value: s.Catalog.fingerprint(), Layer: LayerOf(PathFeatures), Source: PathFeatures})
	}
	if hasStructured {
		catalog, err := parseFeaturesYAML([]byte(structured.Content))
		if err != nil {
			return nil, err
		}
		if !hasMarkdown {
			s.Catalog = catalog
		}
		claims = append(claims, Claim{Key: keyCatalog, Value: catalog.fingerprint(), Layer: LayerOf(PathFeaturesYAML), Source: PathFeaturesYAML})
	}

	settings, err := Resolve(claims)
	if err != nil {
		return nil, err
	}
	version, stated := settings[keySchemaVersion]
	if !stated {
		return nil, hold(reasonInvalid, "%s does not state the schema version of its typed questions", PathDecisions)
	}
	if s.DecisionSchemaVersion, err = strconv.Atoi(version.Value); err != nil || s.DecisionSchemaVersion < 1 {
		return nil, hold(reasonInvalid, "%s states a schema version that is not a positive number", PathDecisions)
	}
	s.Digest = r.bundleDigest()
	return s, nil
}

// reader reads the files of one commit and remembers the first failure, so
// the caller checks once after asking for everything.
type reader struct {
	ctx      context.Context
	source   Source
	revision core.CommitSHA
	digests  map[string]core.Digest
	err      error
}

// optional reads a file that may be absent.
func (r *reader) optional(path string) (Document, bool) {
	if r.err != nil {
		return Document{}, false
	}
	content, err := r.source.ReadFile(r.ctx, r.revision, path)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		return Document{}, false
	case err != nil:
		r.err = hold(reasonUnavailable, "%s could not be read at %s", path, r.revision).WithCause(err)
		return Document{}, false
	case len(content) > MaxFileSize:
		r.err = hold(reasonInvalid, "%s is larger than %d bytes", path, MaxFileSize)
		return Document{}, false
	}
	doc := newDocument(path, content)
	r.digests[path] = doc.Digest
	return doc, true
}

// required reads a file that must exist and say something.
func (r *reader) required(path string) Document {
	doc, found := r.optional(path)
	switch {
	case r.err != nil:
	case !found:
		r.err = hold(reasonMissing, "%s does not exist at %s", path, r.revision)
	case strings.TrimSpace(doc.Content) == "":
		r.err = hold(reasonInvalid, "%s is empty at %s", path, r.revision)
	}
	return doc
}

// bundleDigest hashes the path and the digest of every file read, in path
// order, so any change to any file changes the digest of the whole.
func (r *reader) bundleDigest() core.Digest {
	paths := make([]string, 0, len(r.digests))
	for path := range r.digests {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var b strings.Builder
	b.WriteString("sisf.policy.v1\n")
	for _, path := range paths {
		fmt.Fprintf(&b, "%q %s\n", path, r.digests[path])
	}
	return digestOf([]byte(b.String()))
}

// messageOf returns the caller-safe message of a typed error.
func messageOf(err error) string {
	var typed *core.Error
	if errors.As(err, &typed) {
		return typed.Message
	}
	return "unexpected error"
}
