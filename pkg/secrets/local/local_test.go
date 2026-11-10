package local

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/secrets"
)

// plaintext is a dummy value the tests store, easy to recognise on disk or
// in an error if it ever leaks. Keys are generated per test.
const plaintext = "zone3-test-value-that-must-never-leak"

var webhookRef = secrets.SecretRef{Provider: ProviderName, Name: "github/webhook", Class: secrets.ClassControl, Zone: core.ZoneControl}

// paths returns a store file and a fresh owner-only key file in a temp dir.
func paths(t *testing.T) (store, key string) {
	t.Helper()
	dir := t.TempDir()
	key = filepath.Join(dir, "control", "secret-store.key")
	if err := NewKeyFile(key, nil); err != nil {
		t.Fatalf("NewKeyFile: %v", err)
	}
	return filepath.Join(dir, "control", "secrets.sealed"), key
}

func open(t *testing.T, store, key string) *Store {
	t.Helper()
	s, err := Open(Config{Path: store, KeyFile: key, Zone: core.ZoneControl})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// filled returns an open store that holds plaintext under webhookRef.
func filled(t *testing.T) (s *Store, store, key string) {
	t.Helper()
	store, key = paths(t)
	s = open(t, store, key)
	if err := s.Put(context.Background(), webhookRef, core.NewSecretValue([]byte(plaintext))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return s, store, key
}

func wantCode(t *testing.T, err error, code core.ErrorCode) {
	t.Helper()
	if !core.HasCode(err, code) {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, store, key := filled(t)
	var _ secrets.SecretProvider = s
	var _ secrets.Writer = s

	// A second handle on the same files reads what the first wrote.
	got, err := open(t, store, key).Resolve(ctx, webhookRef)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got.Reveal()) != plaintext {
		t.Fatal("Resolve returned another value")
	}
	refs, err := s.ListRefs(ctx)
	if err != nil || len(refs) != 1 || refs[0] != webhookRef {
		t.Fatalf("ListRefs = %v, %v", refs, err)
	}

	if err := s.Put(ctx, webhookRef, core.NewSecretValue([]byte("replaced"))); err != nil {
		t.Fatalf("Put replace: %v", err)
	}
	got, err = s.Resolve(ctx, webhookRef)
	if err != nil || string(got.Reveal()) != "replaced" {
		t.Fatalf("Resolve after replace: %v", err)
	}

	if err := s.Delete(ctx, webhookRef); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = s.Resolve(ctx, webhookRef)
	wantCode(t, err, core.CodeNotFound)
	wantCode(t, s.Delete(ctx, webhookRef), core.CodeNotFound)
}

func TestCiphertextOnDiskNeverContainsThePlaintext(t *testing.T) {
	t.Parallel()
	_, store, _ := filled(t)
	data, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	for _, form := range []string{
		plaintext,
		base64.StdEncoding.EncodeToString([]byte(plaintext)),
		base64.RawStdEncoding.EncodeToString([]byte(plaintext)),
		base64.URLEncoding.EncodeToString([]byte(plaintext)),
	} {
		if bytes.Contains(data, []byte(form)) {
			t.Fatalf("the store file holds the value in clear (%q)", form)
		}
	}
	if bytes.Contains(data, []byte(webhookRef.Name)) {
		t.Fatal("the store file holds a reference name in clear")
	}
}

func TestTamperedStoreIsDetected(t *testing.T) {
	t.Parallel()
	_, store, key := filled(t)
	original, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	cases := map[string]func([]byte) []byte{
		"header":     func(b []byte) []byte { b[0] ^= 1; return b },
		"nonce":      func(b []byte) []byte { b[len(fileMagic)] ^= 1; return b },
		"ciphertext": func(b []byte) []byte { b[len(fileMagic)+20] ^= 1; return b },
		"tag":        func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		"truncated":  func(b []byte) []byte { return b[:len(b)-5] },
		"too short":  func(b []byte) []byte { return b[:len(fileMagic)+3] },
		"appended":   func(b []byte) []byte { return append(b, 0) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "secrets.sealed")
			if err := os.WriteFile(path, change(bytes.Clone(original)), fileMode); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := Open(Config{Path: path, KeyFile: key, Zone: core.ZoneControl})
			wantCode(t, err, core.CodeTampered)
		})
	}

	// A store already open notices a change made under it.
	s := open(t, store, key)
	changed := bytes.Clone(original)
	changed[len(changed)-1] ^= 1
	if err := os.WriteFile(store, changed, fileMode); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = s.Resolve(context.Background(), webhookRef)
	wantCode(t, err, core.CodeTampered)
}

func TestWrongKeyFailsClosed(t *testing.T) {
	t.Parallel()
	_, store, _ := filled(t)
	otherKey := filepath.Join(t.TempDir(), "other.key")
	if err := NewKeyFile(otherKey, nil); err != nil {
		t.Fatalf("NewKeyFile: %v", err)
	}
	s, err := Open(Config{Path: store, KeyFile: otherKey, Zone: core.ZoneControl})
	wantCode(t, err, core.CodeTampered)
	if s != nil {
		t.Fatal("a store was returned for the wrong key")
	}
}

func TestMalformedKeyFileIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	short := filepath.Join(dir, "short.key")
	if err := os.WriteFile(short, []byte(base64.RawURLEncoding.EncodeToString([]byte("too short"))), fileMode); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Open(Config{Path: filepath.Join(dir, "s"), KeyFile: short, Zone: core.ZoneControl})
	wantCode(t, err, core.CodeInvalidArgument)
	_, err = Open(Config{Path: filepath.Join(dir, "s"), KeyFile: filepath.Join(dir, "absent.key"), Zone: core.ZoneControl})
	wantCode(t, err, core.CodeNotFound)
}

func TestFilesAreOwnerOnly(t *testing.T) {
	t.Parallel()
	_, store, key := filled(t)
	for path, want := range map[string]os.FileMode{store: fileMode, key: fileMode, filepath.Dir(store): dirMode} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s has mode %o, want %o", path, info.Mode().Perm(), want)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(store))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestReadableKeyOrStoreFileIsRefused(t *testing.T) {
	t.Parallel()
	for _, which := range []string{"key", "store"} {
		t.Run(which, func(t *testing.T) {
			t.Parallel()
			_, store, key := filled(t)
			path := map[string]string{"key": key, "store": store}[which]
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			_, err := Open(Config{Path: store, KeyFile: key, Zone: core.ZoneControl})
			wantCode(t, err, core.CodePermissionDenied)
		})
	}
}

func TestNewKeyFileNeverReplacesAKey(t *testing.T) {
	t.Parallel()
	_, key := paths(t)
	before, err := os.ReadFile(key)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	wantCode(t, NewKeyFile(key, nil), core.CodeAlreadyExists)
	after, err := os.ReadFile(key)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("the key file changed")
	}
}

func TestStoreServesOnlyItsZoneAndProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := filled(t)
	agentRef := secrets.SecretRef{Provider: ProviderName, Name: "github/webhook", Class: secrets.ClassRun, Zone: core.ZoneAgent}
	_, err := s.Resolve(ctx, agentRef)
	wantCode(t, err, core.CodePermissionDenied)
	wantCode(t, s.Put(ctx, agentRef, core.NewSecretValue([]byte("x"))), core.CodePermissionDenied)

	envRef := webhookRef
	envRef.Provider = "env"
	_, err = s.Resolve(ctx, envRef)
	wantCode(t, err, core.CodeInvalidArgument)

	escaping := webhookRef
	escaping.Name = "../outside"
	wantCode(t, s.Put(ctx, escaping, core.NewSecretValue([]byte("x"))), core.CodeInvalidArgument)
	wantCode(t, s.Put(ctx, webhookRef, core.SecretValue{}), core.CodeInvalidArgument)

	// The class is part of the reference: asking with another class finds nothing.
	otherClass := webhookRef
	otherClass.Class = secrets.ClassSigning
	_, err = s.Resolve(ctx, otherClass)
	wantCode(t, err, core.CodeNotFound)

	_, err = Open(Config{Path: "x", KeyFile: "y", Zone: core.ZoneShared})
	wantCode(t, err, core.CodeInvalidArgument)
}

func TestValuesNeverAppearInErrorsOrLogs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, store, key := filled(t)
	var errs []error
	agentRef := webhookRef
	agentRef.Zone = core.ZoneAgent
	errs = append(errs, s.Put(ctx, agentRef, core.NewSecretValue([]byte(plaintext))))
	otherKey := filepath.Join(t.TempDir(), "other.key")
	if err := NewKeyFile(otherKey, nil); err != nil {
		t.Fatalf("NewKeyFile: %v", err)
	}
	_, err := Open(Config{Path: store, KeyFile: otherKey, Zone: core.ZoneControl})
	errs = append(errs, err)
	data, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(store, data, fileMode); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = s.Resolve(ctx, webhookRef)
	errs = append(errs, err)
	keyText, err := os.ReadFile(key)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}

	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	value := core.NewSecretValue([]byte(plaintext))
	logger.Info("store", "store", s, "value", value)
	var out strings.Builder
	for _, e := range errs {
		if e == nil {
			t.Fatal("an expected failure returned no error")
		}
		encoded, _ := json.Marshal(e) // a core.Error always encodes
		fmt.Fprintf(&out, "%v %+v %s ", e, e, encoded)
	}
	fmt.Fprintf(&out, "%v %+v %#v %s", s, s, value, logged.String())
	for _, leak := range []string{plaintext, strings.TrimSpace(string(keyText))} {
		if strings.Contains(out.String(), leak) {
			t.Fatalf("a secret appeared in an error or a log line: %s", out.String())
		}
	}
}

func TestConcurrentWritesKeepEveryEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, key := paths(t)
	s := open(t, store, key)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref := webhookRef
			ref.Name = fmt.Sprintf("entry-%d", i)
			if err := s.Put(ctx, ref, core.NewSecretValue([]byte("v"))); err != nil {
				t.Errorf("Put: %v", err)
			}
		}()
	}
	wg.Wait()
	refs, err := s.ListRefs(ctx)
	if err != nil || len(refs) != 8 {
		t.Fatalf("ListRefs = %d entries, %v", len(refs), err)
	}
}
