package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/pkg/core"
)

// renewBefore is how long before its end a delegated key is replaced when
// the supervisor holds the root key and can issue one.
const renewBefore = 24 * time.Hour

// maxKeyFile bounds what is read of a key file on the trust volume.
const maxKeyFile = 64 << 10

// supervisorKeys are the delegated keys the supervisor signs with. Only the
// signers leave this file: QA and release code never sees key material.
type supervisorKeys struct {
	Verifier *delegation.ChainVerifier
	QA       core.Signer
	Artifact core.Signer

	factory  core.FactoryID
	rootPub  ed25519.PublicKey
	rootHeld bool
	// mu guards trusted: every delegation the verifier was given since the
	// supervisor started, kept so that a signature can be judged as of an
	// earlier instant (VerifierAt).
	mu      sync.Mutex
	trusted []delegation.SignedDelegation
}

// remember keeps a delegation the verifier trusts.
func (k *supervisorKeys) remember(d delegation.SignedDelegation) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.trusted = append(k.trusted, d)
}

// Delegation returns the delegation of a key the supervisor has trusted
// since it started, the latest one when the key was trusted twice.
func (k *supervisorKeys) Delegation(keyID string) (delegation.SignedDelegation, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := len(k.trusted) - 1; i >= 0; i-- {
		if k.trusted[i].Delegation.KeyID == keyID {
			return k.trusted[i], true
		}
	}
	return delegation.SignedDelegation{}, false
}

// VerifierAt returns a verifier that judges signatures as of at, trusting
// every delegation the supervisor has trusted since it started. The QA
// receipt of a release that is already verified is judged with it as of
// its Verified step, so a key that expired since does not hold the release
// for good. Only expiry is judged as of at: a key the supervisor's verifier
// holds revoked now is refused whatever the instant, since a revocation
// says the key may have signed what it should not have. A delegation the
// supervisor no longer holds, as one replaced before a restart, is not
// known to it: such a receipt does not verify.
func (k *supervisorKeys) VerifierAt(at time.Time) (core.Verifier, error) {
	v, err := delegation.NewChainVerifier(delegation.VerifierConfig{
		FactoryID: k.factory, Root: k.rootPub, Clock: fixedClock(at), RootKeyInFactoryStore: k.rootHeld,
	})
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	trusted := slices.Clone(k.trusted)
	k.mu.Unlock()
	for _, d := range trusted {
		if err := v.Trust(context.Background(), d); err != nil {
			return nil, err
		}
	}
	return revocationsInForce{pinned: v, now: k.Verifier}, nil
}

// revocationsInForce verifies with a verifier pinned at an instant, after
// refusing a key the verifier in force holds revoked.
type revocationsInForce struct {
	pinned core.Verifier
	now    *delegation.ChainVerifier
}

func (r revocationsInForce) Verify(ctx context.Context, payload []byte, sig core.Signature) error {
	if r.now.Revoked(sig.KeyID) {
		return core.NewError(core.CodeSignatureInvalid, "delegated key "+sig.KeyID+" is revoked")
	}
	return r.pinned.Verify(ctx, payload, sig)
}

// fixedClock is a clock that stands at one instant.
type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }

func (c fixedClock) After(d time.Duration) <-chan time.Time {
	fired := make(chan time.Time, 1)
	fired <- time.Time(c).Add(d)
	return fired
}

// delegationFile and privateKeyFile name a purpose's key files on the trust
// volume, which only the supervisor can write.
func delegationFile(p delegation.Purpose) string { return "delegation-" + string(p) + ".json" }
func privateKeyFile(p delegation.Purpose) string { return "delegation-" + string(p) + ".key" }

// loadSupervisorKeys loads the supervisor's delegated keys from trustDir.
// With root, the root key of the local native profile, it first issues any
// key that is missing or about to expire and keeps it on the trust volume;
// without it, the Owner issued the keys and placed them there. The signers
// do the same again while the supervisor runs.
func loadSupervisorKeys(ctx context.Context, trustDir string, factory core.FactoryID, rootPub ed25519.PublicKey, root core.Signer, clock core.Clock, log *slog.Logger) (*supervisorKeys, error) {
	verifier, err := delegation.NewChainVerifier(delegation.VerifierConfig{
		FactoryID: factory, Root: rootPub, Clock: clock, RootKeyInFactoryStore: root != nil,
	})
	if err != nil {
		return nil, err
	}
	keys := &supervisorKeys{Verifier: verifier, factory: factory, rootPub: rootPub, rootHeld: root != nil}
	load := func(p delegation.Purpose) (core.Signer, error) {
		return newReloadingSigner(ctx, p, func(ctx context.Context) (*delegation.DelegatedSigner, time.Time, error) {
			if root != nil {
				if err := renewKey(ctx, trustDir, factory, p, root, clock); err != nil {
					return nil, time.Time{}, err
				}
			}
			signed, priv, err := readKey(trustDir, p)
			if err != nil {
				return nil, time.Time{}, err
			}
			// Load adds the delegation to the verifier the stages check
			// signatures with before it returns the signer.
			signer, err := delegation.Load(ctx, verifier, delegation.LoadRequest{Holder: delegation.HolderSupervisor, Delegation: signed, PrivateKey: priv})
			if err == nil {
				keys.remember(signed)
			}
			return signer, signed.Delegation.NotAfter, err
		}, clock, log)
	}
	if keys.QA, err = load(delegation.PurposeQAReceipt); err != nil {
		return nil, err
	}
	if keys.Artifact, err = load(delegation.PurposeDeployArtifact); err != nil {
		return nil, err
	}
	return keys, nil
}

// readKey reads one purpose's delegation and private key.
func readKey(trustDir string, p delegation.Purpose) (delegation.SignedDelegation, core.SecretValue, error) {
	var signed delegation.SignedDelegation
	raw, err := readTrustFile(trustDir, delegationFile(p))
	if err != nil {
		return signed, core.SecretValue{}, err
	}
	if err := json.Unmarshal(raw, &signed); err != nil {
		return signed, core.SecretValue{}, stages.Misconfigured(core.NewError(core.CodeInvalidArgument, "the delegation "+delegationFile(p)+" is not readable"))
	}
	priv, err := readTrustFile(trustDir, privateKeyFile(p))
	if err != nil {
		return signed, core.SecretValue{}, err
	}
	key := core.NewSecretValue(priv)
	clear(priv)
	return signed, key, nil
}

// readTrustFile reads a file of the trust volume, refusing anything outside it.
func readTrustFile(trustDir, name string) ([]byte, error) {
	path, err := supervisor.TrustPath(trustDir, name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, core.NewError(core.CodeNotFound, name+" is not on the trust volume")
	}
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable, name+" cannot be read").WithCause(err)
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		clear(buf)
		return nil, core.NewError(core.CodeUnavailable, name+" cannot be read").WithCause(err)
	}
	if len(buf) == 0 || len(buf) > maxKeyFile {
		clear(buf)
		return nil, stages.Misconfigured(core.NewError(core.CodeInvalidArgument, name+" is empty or too large"))
	}
	return buf, nil
}

// renewKey issues a key for p when the trust volume holds none that stays
// valid for renewBefore, and writes it there.
func renewKey(ctx context.Context, trustDir string, factory core.FactoryID, p delegation.Purpose, root core.Signer, clock core.Clock) error {
	now := clock.Now()
	if raw, err := readTrustFile(trustDir, delegationFile(p)); err == nil {
		var signed delegation.SignedDelegation
		if json.Unmarshal(raw, &signed) == nil && signed.Delegation.FactoryID == factory &&
			signed.Delegation.RootKeyID == root.KeyID() && signed.Delegation.NotAfter.Sub(now) > renewBefore {
			return nil
		}
	} else if !core.HasCode(err, core.CodeNotFound) {
		return err
	}
	issued, err := delegation.Issue(ctx, root, delegation.IssueRequest{
		FactoryID: factory, Purpose: p, Holder: delegation.HolderSupervisor, NotBefore: now, Lifetime: delegation.MaxLifetime,
	}, nil)
	if err != nil {
		return err
	}
	doc, err := json.Marshal(issued.Delegation)
	if err != nil {
		return core.NewError(core.CodeInternal, "the delegation cannot be encoded").WithCause(err)
	}
	keyPath, err := supervisor.TrustPath(trustDir, privateKeyFile(p))
	if err != nil {
		return err
	}
	priv := issued.PrivateKey.Reveal()
	defer clear(priv)
	if err := supervisor.WriteTrustFile(keyPath, priv); err != nil {
		return err
	}
	docPath, err := supervisor.TrustPath(trustDir, delegationFile(p))
	if err != nil {
		return err
	}
	return supervisor.WriteTrustFile(docPath, doc)
}

// rootSignerFrom reads the root private key of the local native profile
// from the environment variable name, or returns nil when name is empty.
func rootSignerFrom(name string, lookupEnv func(string) (string, bool)) (core.Signer, error) {
	if name == "" {
		return nil, nil
	}
	value, ok := lookupEnv(name)
	if !ok || value == "" {
		return nil, core.NewError(core.CodeNotFound, name+" holds no root private key")
	}
	priv, err := signing.ParsePrivateKey([]byte(value))
	if err != nil {
		return nil, err
	}
	defer clear(priv)
	return signing.NewSigner(priv)
}
