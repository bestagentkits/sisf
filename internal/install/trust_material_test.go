package install

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// trustDir is an empty trust volume only its owner can write.
func trustDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trust")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func issuedTrust(t *testing.T) (core.FactoryID, map[string][]byte) {
	t.Helper()
	in, _, _ := newTestInstaller(t)
	res := mustInit(t, in, manifest.ModeStandalone)
	release := signing.SignedManifest{Manifest: signing.ReleaseManifest{FactoryID: res.FactoryID}}
	files, err := in.trustMaterial(t.Context(), res.FactoryID, release)
	if err != nil {
		t.Fatal(err)
	}
	return res.FactoryID, files
}

func TestUpIssuesTheSupervisorsDelegatedKeysAndPlacesThemForItsOwnerOnly(t *testing.T) {
	t.Parallel()
	factory, files := issuedTrust(t)
	dir := trustDir(t)
	stale := filepath.Join(dir, promotedLKGPrefix+"app.json")
	must(t, os.WriteFile(stale, []byte("{}"), 0o600))

	env := map[string]string{EnvTrustFiles: encodeTrustFiles(files)}
	must(t, InstallTrustFromEnv(dir, func(k string) string { return env[k] }))

	for _, name := range trustFileNames() {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s is not on the trust volume: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %04o, want 0600", name, info.Mode().Perm())
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the promoted LKG entry of an earlier run was kept")
	}
	got, err := readDelegations(dirReader(dir), factory)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(delegatedPurposes) {
		t.Fatalf("delegations = %+v", got)
	}
	for _, d := range got {
		if want := fixedStart.UTC().Add(delegation.MaxLifetime); !d.NotAfter.Equal(want) {
			t.Errorf("%s expires at %s, want %s", d.Purpose, d.NotAfter, want)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, supervisor.DefaultLKGFile))
	if err != nil || !strings.Contains(string(raw), string(factory)) {
		t.Fatalf("the LKG record does not hold the signed release: %s %v", raw, err)
	}
	if _, err := readDelegations(dirReader(dir), "sisf-another"); err == nil {
		t.Error("the delegations were accepted for another factory")
	}
}

func TestTheTrustStepAcceptsOnlyTheFilesUpPlaces(t *testing.T) {
	t.Parallel()
	_, files := issuedTrust(t)
	good := encodeTrustFiles(files)
	entry := func(name, data string) string { return name + ":" + base64.StdEncoding.EncodeToString([]byte(data)) }
	firstEntry := strings.Split(good, ",")[0]
	for name, value := range map[string]string{
		"nothing":            "",
		"an unknown file":    good + "," + entry("authorized_keys", "x"),
		"a path":             good + "," + entry("../lkg.json", "x"),
		"a file twice":       good + "," + firstEntry,
		"a missing file":     strings.Join(strings.Split(good, ",")[1:], ","),
		"an unreadable file": strings.Replace(good, ":", ":!!", 1),
		"an empty file":      strings.Replace(good, firstEntry, strings.SplitN(firstEntry, ":", 2)[0]+":", 1),
	} {
		if _, err := decodeTrustFiles(value); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := decodeTrustFiles(good); err != nil {
		t.Fatalf("the files up places were refused: %v", err)
	}
}

func TestTheTrustFilesAreNotWrittenWhereOthersCanWrite(t *testing.T) {
	t.Parallel()
	_, files := issuedTrust(t)
	dir := trustDir(t)
	must(t, os.Chmod(dir, 0o770))
	if err := WriteTrustFiles(dir, files); err == nil {
		t.Fatal("the trust files were written to a volume the group can write")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files were left behind: %v", entries)
	}
}

func TestTheDoctorRefusesAMissingDelegatedKey(t *testing.T) {
	t.Parallel()
	factory, files := issuedTrust(t)
	dir := trustDir(t)
	must(t, WriteTrustFiles(dir, files))
	must(t, os.Remove(filepath.Join(dir, "delegation-"+string(delegation.PurposeDeployArtifact)+".key")))
	if _, err := readDelegations(dirReader(dir), factory); err == nil {
		t.Fatal("a delegation without its key was reported present")
	}
}
