package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// recordedAt is when the fixtures say a release became the last known good.
var recordedAt = time.Date(2026, 11, 10, 8, 30, 0, 0, time.UTC)

func TestLKGRecordOutsideTheTrustVolumeIsRefused(t *testing.T) {
	t.Parallel()
	// Each case names a place for the record that a process outside Zone 3
	// could write, or that is not a plain file on the trust volume.
	cases := map[string]func(t *testing.T, d *deployment) string{
		"directory beside the trust volume": func(_ *testing.T, d *deployment) string {
			return filepath.Join(d.dir, "lkg.json")
		},
		"path that climbs out of the trust volume": func(_ *testing.T, d *deployment) string {
			return d.cfg.TrustDir + "/../lkg.json"
		},
		"agent workspace": func(t *testing.T, d *deployment) string {
			workspace := filepath.Join(d.dir, "workspace")
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			return filepath.Join(workspace, "lkg.json")
		},
		"directory inside the trust volume that links outside": func(t *testing.T, d *deployment) string {
			outside := filepath.Join(d.dir, "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			link := filepath.Join(d.cfg.TrustDir, "releases")
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symbolic links are not available: %v", err)
			}
			return filepath.Join(link, "lkg.json")
		},
		"record that is a link to a file outside": func(t *testing.T, d *deployment) string {
			outside := filepath.Join(d.dir, "candidate-lkg.json")
			if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			link := filepath.Join(d.cfg.TrustDir, "lkg.json")
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symbolic links are not available: %v", err)
			}
			return link
		},
		"the trust volume itself": func(_ *testing.T, d *deployment) string {
			return d.cfg.TrustDir
		},
	}
	for name, place := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := newDeployment(t)
			d.cfg.LKGPath = place(t, d)

			sup, err := d.start()
			refused(t, sup, err, core.CodePermissionDenied)

			verifier, err := signing.NewVerifier(d.root)
			if err != nil {
				t.Fatalf("NewVerifier: %v", err)
			}
			store, err := OpenLKGStore(d.cfg.TrustDir, d.cfg.LKGPath, testFactory, verifier)
			if store != nil || !core.HasCode(err, core.CodePermissionDenied) {
				t.Fatalf("OpenLKGStore = %v, %v; want PERMISSION_DENIED", store, err)
			}
		})
	}
}

func TestTrustVolumeThatOthersCanWriteIsRefused(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not describe write access on Windows")
	}
	for name, mode := range map[string]os.FileMode{"group writable": 0o770, "world writable": 0o707} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := newDeployment(t)
			if err := os.Chmod(d.cfg.TrustDir, mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			sup, err := d.start()
			refused(t, sup, err, core.CodePermissionDenied)
		})
	}
}

func TestMissingTrustVolumeIsRefused(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	d.cfg.TrustDir = filepath.Join(d.dir, "absent")
	sup, err := d.start()
	refused(t, sup, err, core.CodeNotFound)

	file := filepath.Join(d.dir, "not-a-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	d.cfg.TrustDir = file
	sup, err = d.start()
	refused(t, sup, err, core.CodeInvalidArgument)

	d.cfg.TrustDir = ""
	sup, err = d.start()
	refused(t, sup, err, core.CodeInvalidArgument)
}

func TestLKGRecordIsKeptOnTheTrustVolume(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	store := d.lkgStore()

	if _, found, err := store.Load(context.Background()); found || err != nil {
		t.Fatalf("Load before any record = found %v, %v; want nothing", found, err)
	}
	record := LKGRecord{Release: d.sign(d.release(), d.signer), RecordedAt: recordedAt}
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, found, err := store.Load(context.Background())
	if err != nil || !found {
		t.Fatalf("Load = found %v, %v", found, err)
	}
	if !loaded.RecordedAt.Equal(recordedAt) || loaded.Release.Manifest.Units[manifest.UnitApp] != appDigest {
		t.Errorf("loaded record = %+v, want the saved one", loaded)
	}

	entries, err := os.ReadDir(d.cfg.TrustDir)
	if err != nil {
		t.Fatalf("read trust directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != DefaultLKGFile {
		t.Errorf("trust directory holds %v, want only %s", entries, DefaultLKGFile)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("LKG record mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestLKGStoreNeverKeepsARecordItCannotVerify(t *testing.T) {
	t.Parallel()
	attacker, _ := newManifestSigner(t)
	cases := map[string]func(d *deployment) LKGRecord{
		"release signed by another key": func(d *deployment) LKGRecord {
			return LKGRecord{Release: d.sign(d.release(), attacker), RecordedAt: recordedAt}
		},
		"unsigned release": func(d *deployment) LKGRecord {
			return LKGRecord{Release: signing.SignedManifest{Manifest: d.release()}, RecordedAt: recordedAt}
		},
		"release of another factory": func(d *deployment) LKGRecord {
			foreign := d.release()
			foreign.FactoryID = "other-factory"
			return LKGRecord{Release: d.sign(foreign, d.signer), RecordedAt: recordedAt}
		},
		"no record time": func(d *deployment) LKGRecord {
			return LKGRecord{Release: d.sign(d.release(), d.signer)}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := newDeployment(t)
			store := d.lkgStore()
			if err := store.Save(context.Background(), build(d)); err == nil {
				t.Fatal("Save accepted a record that cannot be trusted")
			}
			if _, found, err := store.Load(context.Background()); found || err != nil {
				t.Errorf("a refused record left something behind: found %v, %v", found, err)
			}
		})
	}
}

func TestSupervisorReportsWhetherAnLKGIsRecorded(t *testing.T) {
	t.Parallel()
	d := newDeployment(t)
	if lkg := d.mustStart().Status().LKG; lkg.Recorded || len(lkg.Units) != 0 {
		t.Errorf("LKG before any release = %+v, want none recorded", lkg)
	}

	recorded := newDeployment(t)
	record := LKGRecord{Release: recorded.sign(recorded.release(), recorded.signer), RecordedAt: recordedAt}
	if err := recorded.lkgStore().Save(context.Background(), record); err != nil {
		t.Fatalf("Save: %v", err)
	}
	lkg := recorded.mustStart().Status().LKG
	if !lkg.Recorded || !lkg.RecordedAt.Equal(recordedAt) || lkg.Units[manifest.UnitApp] != appDigest {
		t.Errorf("LKG = %+v, want the recorded release", lkg)
	}
}

func TestSupervisorRefusesToStartOnAnLKGRecordItCannotVerify(t *testing.T) {
	t.Parallel()
	attacker, _ := newManifestSigner(t)
	// Each case is what a process that reached the trust volume might leave.
	cases := map[string]func(t *testing.T, d *deployment) []byte{
		"record pointing at a release the Owner never signed": func(t *testing.T, d *deployment) []byte {
			forged := d.release()
			forged.Units[manifest.UnitApp] = otherDigest
			return encodeRecord(t, LKGRecord{Release: d.sign(forged, attacker), RecordedAt: recordedAt})
		},
		"record edited after it was written": func(t *testing.T, d *deployment) []byte {
			good := encodeRecord(t, LKGRecord{Release: d.sign(d.release(), d.signer), RecordedAt: recordedAt})
			return []byte(strings.Replace(string(good), string(appDigest), string(otherDigest), 1))
		},
		"record of another factory": func(t *testing.T, d *deployment) []byte {
			foreign := d.release()
			foreign.FactoryID = "other-factory"
			return encodeRecord(t, LKGRecord{Release: d.sign(foreign, d.signer), RecordedAt: recordedAt})
		},
		"valid record carrying a field the supervisor never writes": func(t *testing.T, d *deployment) []byte {
			good := string(encodeRecord(t, LKGRecord{Release: d.sign(d.release(), d.signer), RecordedAt: recordedAt}))
			extended := strings.Replace(good, `{"release":`, `{"rollback_to":"latest","release":`, 1)
			if extended == good {
				t.Fatal("the fixture does not start the way the record is encoded")
			}
			return []byte(extended)
		},
		"valid record followed by a second one": func(t *testing.T, d *deployment) []byte {
			good := encodeRecord(t, LKGRecord{Release: d.sign(d.release(), d.signer), RecordedAt: recordedAt})
			return append(good, good...)
		},
		"not a record at all": func(*testing.T, *deployment) []byte { return []byte("lkg: latest\n") },
		"empty file":          func(*testing.T, *deployment) []byte { return nil },
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := newDeployment(t)
			path := filepath.Join(d.cfg.TrustDir, DefaultLKGFile)
			if err := os.WriteFile(path, content(t, d), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			sup, err := d.start()
			refused(t, sup, err, core.CodeTampered)
		})
	}
}
