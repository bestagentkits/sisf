package install

import (
	"crypto/ed25519"
	"encoding/base64"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/conformance"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

func TestInitMakesASecretsDirectoryOnlyItsOwnerCanRead(t *testing.T) {
	t.Parallel()
	for _, mode := range []manifest.Mode{manifest.ModeStandalone, manifest.ModeDocker} {
		in, _, _ := newTestInstaller(t)
		mustInit(t, in, mode)
		files := 0
		err := filepath.WalkDir(filepath.Join(in.Layout.Dir, "secrets"), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, _ := d.Info()
			want := secretFileMode
			if d.IsDir() {
				want = secretDirMode
			} else {
				files++
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s: %s has mode %o, want %o", mode, path, info.Mode().Perm(), want)
			}
			return nil
		})
		if err != nil || files == 0 {
			t.Fatalf("%s: walked %d files (%v)", mode, files, err)
		}
		if v, err := CheckSecrets(in.Layout, mode); err != nil || len(v) != 0 {
			t.Errorf("%s: secrets check = %v, %v", mode, v, err)
		}
	}
}

func TestInitNeverPrintsOrReturnsASecretValue(t *testing.T) {
	t.Parallel()
	in, _, out := newTestInstaller(t)
	res := mustInit(t, in, manifest.ModeDocker)

	var secrets []string
	for _, name := range []string{"webhook-secret", "root-key.pem", "github-token"} {
		data, err := os.ReadFile(in.Layout.Secret(areaControl, name))
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, strings.TrimSpace(string(data)))
	}
	for _, z := range core.Zones() {
		data, _ := os.ReadFile(in.Layout.Secret(zoneArea(z), "credentials"))
		secrets = append(secrets, strings.TrimSpace(string(data)))
	}
	shown := out.String() + sprintf("%+v", res)
	for _, w := range res.Warnings {
		shown += w
	}
	if leaked, found := containsAny(shown, secrets...); found {
		t.Fatalf("init output shows a secret value (%d characters)", len(leaked))
	}
	// The manifest holds references, never values.
	manifestText, _ := os.ReadFile(in.Layout.Config())
	if leaked, found := containsAny(string(manifestText), secrets...); found {
		t.Fatalf("factory.yaml holds a secret value (%d characters)", len(leaked))
	}
}

func TestInitWarnsThatTheLocalRootKeyIsLowerAssurance(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	res := mustInit(t, in, manifest.ModeStandalone)
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "lower-assurance") {
		t.Fatalf("warnings do not name the lower assurance of a local root key: %v", res.Warnings)
	}
}

func TestInitKeepsTheRootPrivateKeyInTheControlAreaAndSignsWithIt(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeDocker)
	pem, err := os.ReadFile(in.Layout.Secret(areaControl, "root-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signing.ParsePrivateKey(pem); err != nil {
		t.Fatalf("root key is not a readable ed25519 private key: %v", err)
	}
	for _, area := range []string{areaApplication, areaAgent, areaState, areaSim} {
		if _, err := os.Stat(in.Layout.Secret(area, "root-key.pem")); err == nil {
			t.Errorf("the %s area holds the root private key", area)
		}
	}
}

func TestInitRefusesToOverwriteAnInstall(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	first := mustInit(t, in, manifest.ModeStandalone)
	token, _ := os.ReadFile(in.Layout.Secret(areaControl, "webhook-secret"))

	o := initOptions(in, manifest.ModeStandalone)
	o.FactoryID = "another-factory"
	_, err := in.Init(t.Context(), o)
	var typed *core.Error
	if !asCoreError(err, &typed) || typed.Code != core.CodeAlreadyExists {
		t.Fatalf("second Init = %v, want ALREADY_EXISTS", err)
	}
	after, _ := os.ReadFile(in.Layout.Secret(areaControl, "webhook-secret"))
	if len(token) == 0 || string(after) != string(token) {
		t.Fatal("a refused init changed the webhook secret")
	}
	m, _ := in.Layout.LoadManifest()
	if m.FactoryID != first.FactoryID {
		t.Fatalf("directory now names factory %s, want %s: one install holds exactly one factory_id", m.FactoryID, first.FactoryID)
	}
}

func TestInitWritesNothingThatMakesAnInstallWhenRandomnessFails(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	in.Rand = strings.NewReader("") // exhausted source
	if _, err := in.Init(t.Context(), initOptions(in, manifest.ModeStandalone)); err == nil {
		t.Fatal("Init succeeded without random bytes")
	}
	if done, _ := in.Layout.installed(); done {
		t.Fatal("a failed init left an install behind")
	}
}

func TestInitRecordsTheBudgetConfirmationOnlyWhenTheOwnerGaveIt(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	res := mustInit(t, in, manifest.ModeStandalone)
	rec, ok, err := readBudgetConfirmation(in.Layout.BudgetConfirmation())
	if err != nil || !ok || rec.AlertThresholdMicros != 50_000_000 || rec.Mode != core.BudgetUnlimited {
		t.Fatalf("confirmation = %+v, %v, %v", rec, ok, err)
	}
	if !rec.ConfirmedAt.Equal(fixedStart) {
		t.Fatalf("confirmed at %v, want the clock's %v", rec.ConfirmedAt, fixedStart)
	}
	m, _ := in.Layout.LoadManifest()
	if m.Budget.AlertThresholdMicros != 50_000_000 {
		t.Fatalf("manifest alert threshold = %d", m.Budget.AlertThresholdMicros)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("a confirmed budget produced warnings: %v", res.Warnings)
	}

	other, _, _ := newTestInstaller(t)
	o := initOptions(other, manifest.ModeStandalone)
	o.Budget = BudgetChoice{}
	res, err = other.Init(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := readBudgetConfirmation(other.Layout.BudgetConfirmation()); ok {
		t.Fatal("a confirmation exists though the Owner never confirmed")
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "not confirmed") {
		t.Fatalf("an unconfirmed budget raised no warning: %v", res.Warnings)
	}
}

func TestInitWritesAManifestThePublishedCheckerAccepts(t *testing.T) {
	t.Parallel()
	for _, mode := range []manifest.Mode{manifest.ModeStandalone, manifest.ModeDocker} {
		in, _, _ := newTestInstaller(t)
		mustInit(t, in, mode)
		report, err := conformance.CheckFile(in.Layout.Config())
		if err != nil || report.Err() != nil {
			t.Errorf("%s: written manifest is not conformant: %v %v", mode, err, report.Findings)
		}
	}
}

func TestInitGeneratesServiceUnitsOnlyForStandalone(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	entries, err := os.ReadDir(in.Layout.ServiceDir())
	if err != nil || len(entries) != 2 {
		t.Fatalf("service units = %v, %v, want a launchd and a systemd unit", entries, err)
	}
	unit, _ := os.ReadFile(filepath.Join(in.Layout.ServiceDir(), "sh.sisf.sisf-hello-app.service"))
	// The unit builds from the recorded checkout, starts in it, finds Go,
	// and quotes every path.
	for _, want := range []string{`up --mode standalone --dir "` + in.Layout.Dir + `" --source "` + in.SourceDir + `"`,
		"WorkingDirectory=" + in.SourceDir, `Environment="PATH=/usr/local/go/bin:`, `ExecStart="/usr/local/bin/sisf" up`, "UMask=0077",
		// An up that can never succeed exits 2 and is not started again.
		"Restart=on-failure", "RestartPreventExitStatus=2"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("systemd unit lacks %q:\n%s", want, unit)
		}
	}
	// launchd only tells success from failure, so the agent's up ends a
	// refusal with 0 and launchd starts it again only after a failure.
	plist, _ := os.ReadFile(filepath.Join(in.Layout.ServiceDir(), "sh.sisf.sisf-hello-app.plist"))
	for _, want := range []string{"<string>--refusal-status</string>\n    <string>0</string>",
		"<key>KeepAlive</key>\n  <dict>\n    <key>SuccessfulExit</key><false/>"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("launchd agent lacks %q:\n%s", want, plist)
		}
	}
	docker, _, _ := newTestInstaller(t)
	mustInit(t, docker, manifest.ModeDocker)
	if _, err := os.Stat(docker.Layout.ServiceDir()); err == nil {
		t.Fatal("Docker mode generated service units")
	}
}

func TestInitGivesOnlyTheControlZoneAnApprovalSigningKey(t *testing.T) {
	t.Parallel()
	for _, mode := range []manifest.Mode{manifest.ModeStandalone, manifest.ModeDocker} {
		in, _, _ := newTestInstaller(t)
		mustInit(t, in, mode)
		control, err := readEnvFile(in.Layout.RuntimeEnv(areaControl))
		if err != nil {
			t.Fatal(err)
		}
		seed, err := base64.RawURLEncoding.DecodeString(control["SISF_APPROVAL_KEY"])
		if err != nil || len(seed) != ed25519.SeedSize {
			t.Fatalf("%s: the control zone's approval key is not an ed25519 seed (%d bytes, %v)", mode, len(seed), err)
		}
		for _, area := range []string{areaApplication, areaAgent} {
			env, err := readEnvFile(in.Layout.RuntimeEnv(area))
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := env["SISF_APPROVAL_KEY"]; ok {
				t.Errorf("%s: the %s area holds the approval key", mode, area)
			}
		}
	}
}
