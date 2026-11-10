package install

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
)

// EnvTrustFiles carries the trust volume's files into the one-shot step that
// writes them in Docker mode: comma-separated name:base64 entries. It is
// read from the control zone's trust.env, never from the command line.
const EnvTrustFiles = "SISF_TRUST_FILES"

// delegatedPurposes are the keys the supervisor signs with.
var delegatedPurposes = []delegation.Purpose{delegation.PurposeQAReceipt, delegation.PurposeDeployArtifact}

// trustFileNames are the only files `sisf up` places on the trust volume:
// each purpose's delegation and private key, and the LKG record of the
// release the root key signed at this up.
func trustFileNames() []string {
	names := []string{supervisor.DefaultLKGFile}
	for _, p := range delegatedPurposes {
		names = append(names, "delegation-"+string(p)+".json", "delegation-"+string(p)+".key")
	}
	sort.Strings(names)
	return names
}

// promotedLKGPrefix starts the LKG entries of releases the supervisor
// promoted. `sisf up` installs a newly built release, which is the LKG
// again, so the entries of an earlier run are removed.
const promotedLKGPrefix = "lkg-unit-"

// trustMaterial issues the supervisor's delegated keys with the root key,
// valid from now for the longest lifetime a delegation may have, and makes
// the LKG record of the signed release. It returns file contents by name.
func (in *Installer) trustMaterial(ctx context.Context, factory core.FactoryID, release signing.SignedManifest) (map[string][]byte, error) {
	pem, err := os.ReadFile(in.Layout.Secret(areaControl, "root-key.pem"))
	if err != nil {
		return nil, unavailablef(err, "the root key cannot be read")
	}
	priv, err := signing.ParsePrivateKey(pem)
	clear(pem)
	if err != nil {
		return nil, err
	}
	root, err := signing.NewSigner(priv)
	clear(priv)
	if err != nil {
		return nil, err
	}
	now := in.Clock.Now().UTC()
	files := map[string][]byte{}
	for _, p := range delegatedPurposes {
		issued, err := delegation.Issue(ctx, root, delegation.IssueRequest{
			FactoryID: factory, Purpose: p, Holder: delegation.HolderSupervisor, NotBefore: now, Lifetime: delegation.MaxLifetime,
		}, in.Rand)
		if err != nil {
			return nil, err
		}
		doc, err := json.Marshal(issued.Delegation)
		if err != nil {
			return nil, invalidf("the delegation cannot be encoded")
		}
		files["delegation-"+string(p)+".json"] = doc
		files["delegation-"+string(p)+".key"] = issued.PrivateKey.Reveal()
	}
	record, err := json.MarshalIndent(supervisor.LKGRecord{Release: release, RecordedAt: now}, "", "  ")
	if err != nil {
		return nil, invalidf("the LKG record cannot be encoded")
	}
	files[supervisor.DefaultLKGFile] = append(record, '\n')
	return files, nil
}

// encodeTrustFiles renders files as the value of EnvTrustFiles.
func encodeTrustFiles(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name + ":" + base64.StdEncoding.EncodeToString(files[name])
	}
	return strings.Join(parts, ",")
}

// decodeTrustFiles reads EnvTrustFiles. Only the files `sisf up` places
// are accepted, each once.
func decodeTrustFiles(value string) (map[string][]byte, error) {
	allowed := trustFileNames()
	files := map[string][]byte{}
	for part := range strings.SplitSeq(value, ",") {
		name, encoded, ok := strings.Cut(part, ":")
		if !ok || !slices.Contains(allowed, name) {
			return nil, invalidf("%s holds an entry that is not a trust file", EnvTrustFiles)
		}
		if _, dup := files[name]; dup {
			return nil, invalidf("%s names %s twice", EnvTrustFiles, name)
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(data) == 0 {
			return nil, invalidf("%s holds an unreadable %s", EnvTrustFiles, name)
		}
		files[name] = data
	}
	if len(files) != len(allowed) {
		return nil, invalidf("%s does not hold every trust file", EnvTrustFiles)
	}
	return files, nil
}

// WriteTrustFiles places files on the trust volume dir, for its owner only,
// and removes the promoted LKG entries of an earlier run. The volume must
// exist and be writable by its owner alone, as the supervisor requires.
func WriteTrustFiles(dir string, files map[string][]byte) error {
	for name, data := range files {
		path, err := supervisor.TrustPath(dir, name)
		if err != nil {
			return err
		}
		if err := supervisor.WriteTrustFile(path, data); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return unavailablef(err, "the trust volume cannot be listed")
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), promotedLKGPrefix) && strings.HasSuffix(e.Name(), ".json") {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return unavailablef(err, "an earlier LKG entry cannot be removed")
			}
		}
	}
	return nil
}

// InstallTrustFromEnv is the one-shot step of Docker mode: it reads the
// files from EnvTrustFiles and writes them to the trust volume dir.
func InstallTrustFromEnv(dir string, getenv func(string) string) error {
	files, err := decodeTrustFiles(getenv(EnvTrustFiles))
	if err != nil {
		return err
	}
	defer func() {
		for _, data := range files {
			clear(data)
		}
	}()
	return WriteTrustFiles(dir, files)
}

// DelegationStatus is what the doctor reports of one delegated key.
type DelegationStatus struct {
	Purpose  string
	NotAfter time.Time
}

// trustReader reads one file of the trust volume by name; the doctor reads
// the volume directly in Standalone mode and through the supervisor's
// container in Docker mode.
type trustReader func(name string) ([]byte, error)

// dirReader reads the trust volume at dir.
func dirReader(dir string) trustReader {
	return func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) }
}

// readDelegations reads the delegations on the trust volume and checks that
// each names this factory and the supervisor, and has a private key beside
// it. The keys themselves are read only to see that they are not empty and
// are never kept.
func readDelegations(read trustReader, factory core.FactoryID) ([]DelegationStatus, error) {
	var out []DelegationStatus
	for _, p := range delegatedPurposes {
		raw, err := read("delegation-" + string(p) + ".json")
		if err != nil {
			return nil, unavailablef(err, "the %s delegation is not on the trust volume", p)
		}
		var signed delegation.SignedDelegation
		if err := json.Unmarshal(raw, &signed); err != nil {
			return nil, invalidf("the %s delegation is not readable", p)
		}
		d := signed.Delegation
		if d.FactoryID != factory || d.Purpose != p || d.Holder != delegation.HolderSupervisor {
			return nil, invalidf("the %s delegation is not this factory's supervisor's", p)
		}
		key, err := read("delegation-" + string(p) + ".key")
		present := err == nil && len(key) > 0
		clear(key)
		if !present {
			return nil, invalidf("the %s delegated key is not on the trust volume", p)
		}
		out = append(out, DelegationStatus{Purpose: string(p), NotAfter: d.NotAfter})
	}
	return out, nil
}
