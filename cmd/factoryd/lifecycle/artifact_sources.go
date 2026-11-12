package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// ArtifactSource returns the content of an artifact a run left, checked
// against the artifact's digest.
type ArtifactSource interface {
	Content(ctx context.Context, a agent.Artifact) ([]byte, error)
}

// maxArtifactBytes bounds the content of one artifact read for a build.
const maxArtifactBytes = 1 << 20

// digestOf is the content digest an artifact must carry.
func digestOf(content []byte) core.Digest {
	sum := sha256.Sum256(content)
	return core.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// checked returns content when it is the artifact's content.
func checked(a agent.Artifact, content []byte) ([]byte, error) {
	if digestOf(content) != a.Digest {
		return nil, core.NewError(core.CodeTampered, "the content of artifact "+a.Name+" does not match its digest")
	}
	return content, nil
}

// MemoryArtifacts is a content-addressed artifact store held in memory, for
// the simulation profile, where the scripted agent's patches are known to
// the process that scripts them. It is safe for concurrent use.
type MemoryArtifacts struct {
	mu       sync.Mutex
	contents map[core.Digest][]byte
}

// NewMemoryArtifacts returns an empty store.
func NewMemoryArtifacts() *MemoryArtifacts {
	return &MemoryArtifacts{contents: map[core.Digest][]byte{}}
}

// Put stores content and returns its digest.
func (m *MemoryArtifacts) Put(content []byte) core.Digest {
	d := digestOf(content)
	m.mu.Lock()
	m.contents[d] = append([]byte(nil), content...)
	m.mu.Unlock()
	return d
}

// Content returns the stored content of a, NOT_FOUND when none is stored.
func (m *MemoryArtifacts) Content(_ context.Context, a agent.Artifact) ([]byte, error) {
	m.mu.Lock()
	content, ok := m.contents[a.Digest]
	m.mu.Unlock()
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "no content is stored for artifact "+a.Name)
	}
	return checked(a, content)
}

// FileArtifacts reads artifacts the CLI agent backends wrote, from file://
// URIs below Root. A URI outside Root is refused.
type FileArtifacts struct {
	Root string
}

// Content reads the artifact's file and checks it against its digest.
func (f FileArtifacts) Content(_ context.Context, a agent.Artifact) ([]byte, error) {
	u, err := url.Parse(a.URI)
	if err != nil || u.Scheme != "file" || f.Root == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "artifact "+a.Name+" is not a file below the artifact directory")
	}
	root, err := filepath.Abs(f.Root)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument, "the artifact directory cannot be resolved")
	}
	p := filepath.Clean(u.Path)
	if rel, err := filepath.Rel(root, p); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil, core.NewError(core.CodePermissionDenied, "artifact "+a.Name+" lies outside the artifact directory")
	}
	file, err := os.Open(p)
	if err != nil {
		return nil, core.NewError(core.CodeNotFound, "artifact "+a.Name+" cannot be opened").WithCause(err)
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxArtifactBytes+1))
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable, "artifact "+a.Name+" cannot be read").WithCause(err)
	}
	if len(content) > maxArtifactBytes {
		return nil, core.NewError(core.CodeInvalidArgument, "artifact "+a.Name+" is too large to build")
	}
	return checked(a, content)
}
