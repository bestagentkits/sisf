package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// receiptPayload is the opening of a QA receipt's signing payload.
func receiptPayload() []byte { return core.NewPayloadBuilder("sisf.qa_receipt", 1).Bytes() }

// logLines collects what the supervisor logs, safely for the race detector.
type logLines struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logLines) count(text string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.buf.String(), text)
}

func (l *logLines) logger() *slog.Logger { return slog.New(slog.NewTextHandler(l, nil)) }

// A supervisor that holds the root key and runs past the end of its
// delegated keys replaces them and keeps signing: the verifier it shares
// with the stages accepts what the replacement signs.
func TestASupervisorHoldingTheRootKeyRenewsItsDelegatedKeysWhileItRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := sqlstoretest.NewClock(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC))
	dir, pub, root := trustVolume(t)
	keys, err := loadSupervisorKeys(ctx, dir, testFactory, pub, root, clock, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	first, err := keys.QA.Sign(ctx, receiptPayload())
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(delegation.MaxLifetime - renewBefore/2)
	early, err := keys.QA.Sign(ctx, receiptPayload())
	if err != nil {
		t.Fatalf("signing a day before the key ends: %v", err)
	}
	if early.KeyID == first.KeyID {
		t.Fatal("a key about to expire was not replaced while the supervisor ran")
	}

	clock.Advance(delegation.MaxLifetime)
	late, err := keys.QA.Sign(ctx, receiptPayload())
	if err != nil {
		t.Fatalf("signing after the first two keys ended: %v", err)
	}
	if late.KeyID == early.KeyID || late.KeyID != keys.QA.KeyID() {
		t.Fatalf("signed with %s, the signer names %s, the key before was %s", late.KeyID, keys.QA.KeyID(), early.KeyID)
	}
	if err := keys.Verifier.Verify(ctx, receiptPayload(), late); err != nil {
		t.Fatalf("the verifier does not know the replacement key: %v", err)
	}
	if _, err := keys.Artifact.Sign(ctx, core.NewPayloadBuilder("sisf.deploy_artifact", 1).Bytes()); err != nil {
		t.Fatalf("the artifact key was not replaced: %v", err)
	}
}

// A supervisor without the root key picks up the keys the Owner places on
// the trust volume, within the reload interval and not before.
func TestASupervisorWithoutTheRootKeyPicksUpKeysTheOwnerReplaced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := sqlstoretest.NewClock(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC))
	dir, pub, root := trustVolume(t)
	if _, err := loadSupervisorKeys(ctx, dir, testFactory, pub, root, clock, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	keys, err := loadSupervisorKeys(ctx, dir, testFactory, pub, nil, clock, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	first := keys.QA.KeyID()

	// The Owner issues a replacement and places it on the volume.
	if err := os.Remove(filepath.Join(dir, delegationFile(delegation.PurposeQAReceipt))); err != nil {
		t.Fatal(err)
	}
	if err := renewKey(ctx, dir, testFactory, delegation.PurposeQAReceipt, root, clock); err != nil {
		t.Fatal(err)
	}
	clock.Advance(keyReloadEvery / 2)
	sig, err := keys.QA.Sign(ctx, receiptPayload())
	if err != nil || sig.KeyID != first {
		t.Fatalf("the volume was read again before the interval: key %s, %v", sig.KeyID, err)
	}
	clock.Advance(keyReloadEvery)
	sig, err = keys.QA.Sign(ctx, receiptPayload())
	if err != nil || sig.KeyID == first {
		t.Fatalf("the replaced key was not picked up: key %s, %v", sig.KeyID, err)
	}
	if err := keys.Verifier.Verify(ctx, receiptPayload(), sig); err != nil {
		t.Fatalf("the verifier does not know the replacement key: %v", err)
	}
}

// A reload that fails keeps the key in use and says so once per retry
// interval; once that key has ended nothing is signed.
func TestAFailedReloadKeepsTheCurrentKeyAndNeverSignsWithAnExpiredOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := sqlstoretest.NewClock(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC))
	dir, pub, root := trustVolume(t)
	if _, err := loadSupervisorKeys(ctx, dir, testFactory, pub, root, clock, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	lines := &logLines{}
	keys, err := loadSupervisorKeys(ctx, dir, testFactory, pub, nil, clock, lines.logger())
	if err != nil {
		t.Fatal(err)
	}
	first := keys.QA.KeyID()
	if err := os.WriteFile(filepath.Join(dir, delegationFile(delegation.PurposeQAReceipt)), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	clock.Advance(2 * keyReloadEvery)
	for range 3 {
		sig, err := keys.QA.Sign(ctx, receiptPayload())
		if err != nil || sig.KeyID != first {
			t.Fatalf("a failed reload did not keep the current key: key %s, %v", sig.KeyID, err)
		}
	}
	const failed = "a delegated key could not be reloaded"
	if got := lines.count(failed); got != 1 {
		t.Fatalf("the failed reload was logged %d times, want once per retry interval", got)
	}
	clock.Advance(keyReloadRetry)
	if _, err := keys.QA.Sign(ctx, receiptPayload()); err != nil {
		t.Fatal(err)
	}
	if got := lines.count(failed); got != 2 {
		t.Fatalf("the reload was not tried again after the retry interval: %d log lines", got)
	}
	if lines.count("PRIVATE KEY") != 0 {
		t.Fatal("the log carries key material")
	}

	clock.Advance(delegation.MaxLifetime)
	if _, err := keys.QA.Sign(ctx, receiptPayload()); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("an expired key with no replacement: got %v, want PERMISSION_DENIED", err)
	}
}
