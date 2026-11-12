package cliagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// writeArtifact stores data under dir and describes it. The file is private
// to the factory user.
func writeArtifact(dir, name string, kind agent.ArtifactKind, data []byte) (agent.Artifact, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return agent.Artifact{}, core.NewError(core.CodeInternal, "the artifact directory could not be created")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return agent.Artifact{}, core.NewError(core.CodeInternal, "an artifact could not be written")
	}
	sum := sha256.Sum256(data)
	return agent.Artifact{
		Name: name, Kind: kind,
		Digest: core.Digest("sha256:" + hex.EncodeToString(sum[:])),
		URI:    "file://" + filepath.ToSlash(path), Size: int64(len(data)),
	}, nil
}

// transcript renders events as JSON lines. Events are already redacted.
func transcript(events []agent.Event) []byte {
	var out []byte
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			continue
		}
		out = append(append(out, line...), '\n')
	}
	return out
}
