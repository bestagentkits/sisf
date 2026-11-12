package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

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
	QA       *delegation.DelegatedSigner
	Artifact *delegation.DelegatedSigner
}

// delegationFile and privateKeyFile name a purpose's key files on the trust
// volume, which only the supervisor can write.
func delegationFile(p delegation.Purpose) string { return "delegation-" + string(p) + ".json" }
func privateKeyFile(p delegation.Purpose) string { return "delegation-" + string(p) + ".key" }

// loadSupervisorKeys loads the supervisor's delegated keys from trustDir.
// With root, the root key of the local native profile, it first issues any
// key that is missing or about to expire and keeps it on the trust volume;
// without it, the Owner issued the keys and placed them there.
func loadSupervisorKeys(ctx context.Context, trustDir string, factory core.FactoryID, rootPub ed25519.PublicKey, root core.Signer, clock core.Clock) (*supervisorKeys, error) {
	verifier, err := delegation.NewChainVerifier(delegation.VerifierConfig{
		FactoryID: factory, Root: rootPub, Clock: clock, RootKeyInFactoryStore: root != nil,
	})
	if err != nil {
		return nil, err
	}
	load := func(p delegation.Purpose) (*delegation.DelegatedSigner, error) {
		if root != nil {
			if err := renewKey(ctx, trustDir, factory, p, root, clock); err != nil {
				return nil, err
			}
		}
		signed, priv, err := readKey(trustDir, p)
		if err != nil {
			return nil, err
		}
		return delegation.Load(ctx, verifier, delegation.LoadRequest{Holder: delegation.HolderSupervisor, Delegation: signed, PrivateKey: priv})
	}
	keys := &supervisorKeys{Verifier: verifier}
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
		return signed, core.SecretValue{}, core.NewError(core.CodeInvalidArgument, "the delegation "+delegationFile(p)+" is not readable")
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
	if err != nil || len(buf) == 0 || len(buf) > maxKeyFile {
		clear(buf)
		return nil, core.NewError(core.CodeInvalidArgument, name+" is empty, unreadable or too large")
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
